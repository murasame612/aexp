#!/usr/bin/env python3
"""Index one verified corpus into the currently selected LightRAG workspace."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

try:
    from .validate_revision import validate
except ImportError:  # Direct script execution on the deployed host.
    from validate_revision import validate


def canonicalize_rows(source_rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Select one deterministic representative for each exact text hash."""
    by_content: dict[str, dict[str, Any]] = {}
    for row in sorted(source_rows, key=lambda value: (value["sha256"], value["file_source"])):
        sanitized_text = row["text"].strip()
        content_key = hashlib.md5(sanitized_text.encode(), usedforsecurity=False).hexdigest()
        by_content.setdefault(content_key, {**row, "text": sanitized_text})
    return list(by_content.values())


def select_rows(
    source_rows: list[dict[str, Any]],
    provenance: dict[str, dict[str, Any]],
    profile: str,
    pdf_chunks_per_paper: int,
) -> list[dict[str, Any]]:
    """Choose a bounded, paper-balanced shadow index or the complete corpus."""
    if profile == "full":
        return source_rows
    annotations = [row for row in source_rows if row.get("kind") == "annotation"]
    per_paper: dict[str, list[dict[str, Any]]] = {}
    for row in source_rows:
        if row.get("kind") != "pdf_chunk":
            continue
        source = provenance[row["file_source"]]
        per_paper.setdefault(source["zotero_item_key"], []).append(row)
    selected = list(annotations)
    for rows in per_paper.values():
        ordered = sorted(
            rows,
            key=lambda row: (
                provenance[row["file_source"]].get("page") or 0,
                provenance[row["file_source"]].get("page_chunk_index") or 0,
                row["file_source"],
            ),
        )
        count = min(pdf_chunks_per_paper, len(ordered))
        if count == 1:
            indices = [len(ordered) // 2]
        else:
            indices = [round(index * (len(ordered) - 1) / (count - 1)) for index in range(count)]
        selected.extend(ordered[index] for index in indices)
    return selected


def request_json(url: str, api_key: str, body: dict[str, Any] | None = None) -> tuple[int, dict[str, Any]]:
    headers = {"X-API-Key": api_key}
    data = None
    method = "GET"
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body, ensure_ascii=False).encode()
        method = "POST"
    request = urllib.request.Request(url, headers=headers, data=data, method=method)
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read() or b"{}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("revision", type=Path)
    parser.add_argument("--upstream", default="http://127.0.0.1:9621")
    parser.add_argument("--batch-size", type=int, default=20)
    parser.add_argument("--timeout", type=int, default=14_400)
    parser.add_argument("--result", type=Path)
    parser.add_argument("--profile", choices=("shadow", "full"), default="shadow")
    parser.add_argument("--pdf-chunks-per-paper", type=int, default=4)
    args = parser.parse_args()
    verified = validate(args.revision)
    api_key = os.environ["LIGHTRAG_API_KEY"]
    source_rows = [json.loads(line) for line in (args.revision / "documents.jsonl").read_text().splitlines() if line]
    provenance = json.loads((args.revision / "provenance.json").read_text())
    selected_rows = select_rows(source_rows, provenance, args.profile, args.pdf_chunks_per_paper)
    # Zotero collections may intentionally contain original/compare PDF copies.
    # LightRAG identifies documents by content hash and reports repeated content
    # as failed records, so canonicalize identical text only in the disposable
    # derived index.  The immutable corpus and provenance retain every source.
    rows = canonicalize_rows(selected_rows)
    print(
        json.dumps(
            {
                "status": "canonicalized",
                "source_documents": len(source_rows),
                "selected_source_documents": len(selected_rows),
                "index_documents": len(rows),
                "duplicates_omitted": len(selected_rows) - len(rows),
                "profile": args.profile,
            },
            ensure_ascii=False,
        ),
        flush=True,
    )
    track_ids: list[str] = []
    for start in range(0, len(rows), args.batch_size):
        batch = rows[start : start + args.batch_size]
        status, payload = request_json(
            args.upstream.rstrip("/") + "/documents/texts",
            api_key,
            {
                "texts": [row["text"] for row in batch],
                "file_sources": [row["file_source"] for row in batch],
                "chunking": {
                    "strategy": "fixed_token",
                    "params": {
                        "chunk_token_size": 1800,
                        "chunk_overlap_token_size": 0,
                        "split_by_character": "\n\n",
                        "split_by_character_only": True,
                    },
                },
            },
        )
        if status >= 300:
            raise SystemExit(f"index enqueue failed at {start}: HTTP {status} {payload}")
        if payload.get("track_id"):
            track_ids.append(payload["track_id"])

    deadline = time.monotonic() + args.timeout
    last_counts: dict[str, int] = {}
    while time.monotonic() < deadline:
        status, payload = request_json(args.upstream.rstrip("/") + "/documents/status_counts", api_key)
        if status >= 300:
            raise SystemExit(f"status request failed: HTTP {status} {payload}")
        counts = {str(key).lower(): int(value) for key, value in payload.get("status_counts", {}).items()}
        if counts != last_counts:
            print(json.dumps({"status": "indexing", "counts": counts}, ensure_ascii=False), flush=True)
            last_counts = counts
        failed = counts.get("failed", 0)
        processed = counts.get("processed", 0)
        # LightRAG exposes ``all`` as a summary count alongside the actual
        # lifecycle states.  Treating it as active work makes a completed
        # index wait until the outer timeout even when processed == all.
        active = sum(value for key, value in counts.items() if key not in {"processed", "failed", "all"})
        if failed:
            detail_status, detail = request_json(
                args.upstream.rstrip("/") + "/documents/paginated",
                api_key,
                {"status_filters": ["failed"], "page": 1, "page_size": min(200, failed)},
            )
            samples = []
            if detail_status < 300:
                samples = [
                    {"id": row.get("id"), "file_path": row.get("file_path"), "error_msg": row.get("error_msg")}
                    for row in detail.get("documents", [])[:10]
                ]
            raise SystemExit(json.dumps({"status": "failed", "failed": failed, "samples": samples}, ensure_ascii=False))
        if processed == len(rows) and active == 0:
            result = {
                **verified,
                "status": "indexed",
                "source_documents": len(source_rows),
                "selected_source_documents": len(selected_rows),
                "index_documents": len(rows),
                "duplicates_omitted": len(selected_rows) - len(rows),
                "profile": args.profile,
                "pdf_chunks_per_paper": args.pdf_chunks_per_paper if args.profile == "shadow" else None,
                "tracks": track_ids,
                "status_counts": counts,
            }
            result_path = args.result or (args.revision / "index-result.json")
            result_path.parent.mkdir(parents=True, exist_ok=True)
            result_path.write_text(json.dumps(result, ensure_ascii=False, sort_keys=True, indent=2) + "\n")
            print(json.dumps(result, ensure_ascii=False))
            return
        time.sleep(5)
    raise SystemExit(f"index timeout after {args.timeout}s; last counts={last_counts}")


if __name__ == "__main__":
    main()
