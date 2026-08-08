#!/usr/bin/env python3
"""Thin authenticated provenance gateway for literature retrieval backends."""

from __future__ import annotations

import argparse
import hmac
import json
import os
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


def post_json(url: str, body: dict[str, Any], api_key: str) -> tuple[int, dict[str, Any]]:
    request = urllib.request.Request(
        url,
        data=json.dumps(body, ensure_ascii=False).encode(),
        headers={"Content-Type": "application/json", "X-API-Key": api_key},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        try:
            payload = json.loads(error.read())
        except (json.JSONDecodeError, UnicodeDecodeError):
            payload = {"detail": "upstream request failed"}
        return error.code, payload
    except urllib.error.URLError as error:
        return 503, {"detail": f"upstream unavailable: {error.reason}"}


class Gateway:
    def __init__(
        self,
        state_root: Path,
        upstream: str,
        upstream_key: str,
        token: str,
        *,
        paperqa_upstream: str | None = None,
        paperqa_key: str | None = None,
        primary_backend: str = "paperqa2",
    ):
        self.state_root = state_root
        self.upstream = upstream.rstrip("/")
        self.upstream_key = upstream_key
        self.token = token
        self.paperqa_upstream = (paperqa_upstream or "http://127.0.0.1:9631").rstrip("/")
        self.paperqa_key = paperqa_key or upstream_key
        if primary_backend != "paperqa2":
            raise ValueError(f"unsupported primary backend: {primary_backend}")
        self.primary_backend = primary_backend

    def state(self, corpus_revision: str = "") -> tuple[dict[str, Any], dict[str, Any]]:
        active_path = self.state_root / "active.json"
        active = json.loads(active_path.read_text()) if active_path.is_file() else {}
        revision = corpus_revision or str(active.get("corpus_revision") or "")
        if not revision:
            return {"status": "installed", "freshness": "unknown"}, {}
        revision_root = self.state_root / "corpora" / revision
        if not (revision_root / "manifest.json").is_file():
            return {"status": "missing", "freshness": "unknown", "corpus_revision": revision}, {}
        manifest = json.loads((revision_root / "manifest.json").read_text())
        provenance = json.loads((revision_root / "provenance.json").read_text())
        paperqa = self.paperqa_state(revision)
        selected = dict(active) if active.get("corpus_revision") == revision else {}
        active_status = str(selected.get("status") or "")
        selected.update({
            "status": active_status if active_status in {"indexed", "shadow", "accepted", "stale"}
            else ("indexed" if paperqa.get("status") == "indexed" else "published"),
            "freshness": selected.get("freshness", "frozen"),
            "corpus_revision": revision,
            "manifest": manifest,
        })
        return selected, provenance

    def paperqa_state(self, corpus_revision: str = "") -> dict[str, Any]:
        if corpus_revision:
            candidates = sorted(
                (self.state_root / "paperqa").glob(f"{corpus_revision}-*/index-result.json"),
                key=lambda path: ("-hybrid/" not in str(path), str(path)),
            )
            if not candidates:
                return {"status": "published", "backend": "paperqa2", "corpus_revision": corpus_revision}
            result = json.loads(candidates[0].read_text())
            return {**result, "workspace": str(candidates[0].parent.relative_to(self.state_root))}
        path = self.state_root / "paperqa-active.json"
        if not path.is_file():
            return {"status": "installed", "backend": "paperqa2"}
        return json.loads(path.read_text())

    def _upstream_query(
        self,
        backend: str,
        query: str,
        body: dict[str, Any],
        corpus_revision: str,
    ) -> tuple[int, dict[str, Any], dict[str, Any]]:
        if backend == "paperqa2":
            upstream_body = {
                "query": query,
                "corpus_revision": corpus_revision,
                "evidence_k": body.get("evidence_k", 10),
                "answer_max_sources": body.get("answer_max_sources", 6),
            }
            status, response = post_json(self.paperqa_upstream + "/query", upstream_body, self.paperqa_key)
            return status, response, upstream_body
        upstream_body = {
            "query": query,
            "mode": body.get("mode", "mix"),
            "include_references": True,
            "include_chunk_content": True,
            "enable_rerank": body.get("enable_rerank", True),
        }
        status, response = post_json(self.upstream + "/query", upstream_body, self.upstream_key)
        return status, response, upstream_body

    def query(self, body: dict[str, Any]) -> tuple[int, dict[str, Any]]:
        requested_revision = str(body.get("corpus_revision") or "").strip()
        state, provenance = self.state(requested_revision)
        if state.get("status") not in {"indexed", "shadow", "accepted", "stale"}:
            return 503, {"status": "blocked", "code": "NO_ACTIVE_VERIFIED_INDEX", "state": state.get("status", "installed")}
        query = str(body.get("query", "")).strip()
        if len(query) < 3:
            return 400, {"status": "failed", "code": "QUERY_TOO_SHORT"}
        backend = str(body.get("backend") or self.primary_backend)
        if backend not in {"paperqa2", "lightrag"}:
            return 400, {"status": "failed", "code": "UNKNOWN_RETRIEVAL_BACKEND", "backend": backend}
        if backend == "lightrag":
            return 409, {
                "status": "blocked",
                "code": "LIGHTRAG_NOT_ENABLED",
                "backend": "lightrag",
                "detail": "LightRAG is frozen and not part of the ResearchOS default literature path.",
            }
        status, response, upstream_body = self._upstream_query(backend, query, body, state["corpus_revision"])
        if status >= 400:
            return status, {
                "status": "failed",
                "code": "PAPERQA_UPSTREAM" if backend == "paperqa2" else "LIGHTRAG_UPSTREAM",
                "backend": backend,
                "detail": response.get("detail", "upstream request failed"),
            }
        evidence = []
        warnings = []
        # Older derived indexes may return only the normalized basename while
        # the frozen corpus stores a logical path. The publisher/validator
        # guarantees global basename uniqueness, so this compatibility lookup
        # cannot silently pick the wrong Zotero source.
        provenance_by_basename = {Path(key).name: (key, value) for key, value in provenance.items()}
        for reference in response.get("references") or []:
            file_source = reference.get("file_path", "")
            source = provenance.get(file_source)
            canonical_source = file_source
            if not source:
                resolved = provenance_by_basename.get(Path(file_source).name)
                if resolved:
                    canonical_source, source = resolved
            if not source:
                warnings.append({"code": "REFERENCE_PROVENANCE_MISSING", "file_source": file_source})
                continue
            evidence.append(
                {
                    **source,
                    "file_source": canonical_source,
                    "text": reference.get("content", []),
                    "reference_id": reference.get("reference_id"),
                    "score": reference.get("score"),
                }
            )
        answer = response.get("response", "")
        answerability = response.get("answerability")
        if answerability not in {"supported", "partial", "unsupported"}:
            answerability = "partial" if evidence and str(answer).strip() else "unsupported"
        backend_warnings = list(state.get("warnings", []))
        if backend == "paperqa2":
            backend_warnings = [warning for warning in backend_warnings if warning.get("code") != "PARTIAL_INDEX_COVERAGE"]
        result = {
            "answer": answer,
            "answerability": answerability,
            "evidence_domain": "literature",
            "claim_scope": "background_only",
            "retrieval_backend": backend,
            "retrieval_mode": response.get("retrieval_mode") or upstream_body.get("mode", "paperqa2-direct"),
            "fallback_used": False,
            "corpus_revision": state["corpus_revision"],
            "zotero_collection_key": state["manifest"].get("source", {}).get("collection_key"),
            "snapshot_sha256": "sha256:" + state["manifest"]["manifest_sha256"],
            "freshness": state.get("freshness", "unknown"),
            "evidence": evidence,
            "warnings": warnings + backend_warnings,
        }
        return 200, result


def handler_for(gateway: Gateway):
    class Handler(BaseHTTPRequestHandler):
        server_version = "ResearchOS-Literature/1"

        def log_message(self, fmt: str, *args: object) -> None:
            print(f"{self.address_string()} {fmt % args}")

        def send_json(self, status: int, payload: dict[str, Any]) -> None:
            encoded = json.dumps(payload, ensure_ascii=False).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def authorized(self) -> bool:
            supplied = self.headers.get("Authorization", "")
            expected = "Bearer " + gateway.token
            return hmac.compare_digest(supplied, expected)

        def do_GET(self) -> None:
            if not self.authorized():
                self.send_json(401, {"detail": "unauthorized"})
                return
            parsed = urllib.parse.urlparse(self.path)
            if parsed.path != "/health":
                self.send_json(404, {"detail": "not found"})
                return
            requested_revision = urllib.parse.parse_qs(parsed.query).get("corpus_revision", [""])[0]
            state, _ = gateway.state(requested_revision)
            self.send_json(
                200,
                {
                    "status": "ready" if state.get("status") == "indexed" else state.get("status", "installed"),
                    "primary_backend": gateway.primary_backend,
                    "corpus_revision": state.get("corpus_revision"),
                    "zotero_collection_key": state.get("manifest", {}).get("source", {}).get("collection_key"),
                    "freshness": state.get("freshness", "unknown"),
                    "backends": {
                        "paperqa2": gateway.paperqa_state(str(state.get("corpus_revision") or "")),
                    },
                },
            )

        def do_POST(self) -> None:
            if not self.authorized():
                self.send_json(401, {"detail": "unauthorized"})
                return
            if self.path != "/query":
                self.send_json(404, {"detail": "not found"})
                return
            try:
                length = min(int(self.headers.get("Content-Length", "0")), 1024 * 1024)
                body = json.loads(self.rfile.read(length))
            except (ValueError, json.JSONDecodeError):
                self.send_json(400, {"detail": "invalid JSON"})
                return
            status, payload = gateway.query(body)
            self.send_json(status, payload)

    return Handler


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default=os.getenv("ZOTERO_LIGHTRAG_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.getenv("ZOTERO_LIGHTRAG_PORT", "8766")))
    parser.add_argument("--state-root", type=Path, default=Path(os.getenv("ZOTERO_LIGHTRAG_STATE_ROOT", ".")))
    parser.add_argument("--upstream", default=os.getenv("ZOTERO_LIGHTRAG_UPSTREAM", "http://127.0.0.1:9621"))
    parser.add_argument("--paperqa-upstream", default=os.getenv("PAPERQA_UPSTREAM", "http://127.0.0.1:9631"))
    parser.add_argument("--primary-backend", choices=("paperqa2",), default="paperqa2")
    args = parser.parse_args()
    token = os.environ["ZOTERO_LIGHTRAG_TOKEN"]
    upstream_key = os.environ.get("LIGHTRAG_API_KEY", "disabled")
    gateway = Gateway(
        args.state_root,
        args.upstream,
        upstream_key,
        token,
        paperqa_upstream=args.paperqa_upstream,
        paperqa_key=os.environ.get("PAPERQA_API_KEY", upstream_key),
        primary_backend=args.primary_backend,
    )
    ThreadingHTTPServer((args.host, args.port), handler_for(gateway)).serve_forever()


if __name__ == "__main__":
    main()
