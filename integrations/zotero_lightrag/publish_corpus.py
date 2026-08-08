#!/usr/bin/env python3
"""Publish an immutable, page-addressable Zotero corpus revision."""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import os
import re
import shutil
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any, Iterable


SCHEMA = "researchos-zotero-corpus/v1"
BASE = "http://127.0.0.1:23119/api/users/0"
HEADERS = {"Zotero-API-Version": "3"}
PUBLISHER_VERSION = "1.0.1"
PARSER = "zotero-fulltext-api/form-feed-pages-v1"
CHUNK_SIZE = 1_400
CHUNK_OVERLAP = 220
UF_DATALESS = 0x40000000


class SourceChanged(RuntimeError):
    """The Zotero library changed while a revision was being collected."""


def canonical_json(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode()


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def get(path: str) -> tuple[bytes, dict[str, str], int]:
    request = urllib.request.Request(BASE + path, headers=HEADERS)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.read(), dict(response.headers.items()), response.status
    except urllib.error.HTTPError as error:
        return error.read(), dict(error.headers.items()), error.code


def get_json(path: str) -> tuple[Any, dict[str, str]]:
    body, headers, status = get(path)
    if status >= 400:
        raise RuntimeError(f"Zotero Local API returned HTTP {status} for {path}")
    return json.loads(body), headers


def current_library_version() -> int:
    _, headers = get_json("/items?limit=1&sort=dateModified&direction=desc")
    return int(headers.get("Last-Modified-Version") or 0)


def clean(text: str) -> str:
    text = html.unescape(re.sub(r"<[^>]+>", " ", text or ""))
    return re.sub(r"\s+", " ", text).strip()


def creator_names(data: dict[str, Any]) -> list[str]:
    names: list[str] = []
    for creator in data.get("creators", []):
        name = clean(" ".join(filter(None, [creator.get("firstName"), creator.get("lastName")])))
        if not name:
            name = clean(creator.get("name", ""))
        if name:
            names.append(name)
    return names


def metadata_text(data: dict[str, Any]) -> str:
    fields = [
        ("Title", clean(data.get("title", ""))),
        ("Abstract", clean(data.get("abstractNote", ""))),
        ("Publication", clean(data.get("publicationTitle", ""))),
        ("Date", clean(data.get("date", ""))),
        ("DOI", clean(data.get("DOI", ""))),
        ("URL", clean(data.get("url", ""))),
    ]
    return "\n".join(f"{label}: {value}" for label, value in fields if value)


def chunks(text: str, size: int = CHUNK_SIZE, overlap: int = CHUNK_OVERLAP) -> Iterable[str]:
    text = clean(text)
    cursor = 0
    while text and cursor < len(text):
        end = min(len(text), cursor + size)
        yield text[cursor:end]
        if end == len(text):
            break
        cursor = max(cursor + 1, end - overlap)


def page_chunks(text: str) -> Iterable[tuple[int, int, str]]:
    for page_index, page in enumerate((text or "").split("\f")):
        for page_chunk_index, body in enumerate(chunks(page)):
            yield page_index, page_chunk_index, body


def all_pages(path: str) -> list[dict[str, Any]]:
    output: list[dict[str, Any]] = []
    start = 0
    while True:
        separator = "&" if "?" in path else "?"
        payload, _ = get_json(f"{path}{separator}limit=100&start={start}")
        output.extend(payload)
        if len(payload) < 100:
            return output
        start += len(payload)


def local_attachment_path(key: str) -> Path | None:
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
            return None

    request = urllib.request.Request(BASE + f"/items/{urllib.parse.quote(key)}/file", headers=HEADERS)
    # The Local API deliberately redirects to a file:// URL.  Capture that
    # location instead of asking urllib to follow a non-HTTP URL.
    opener = urllib.request.build_opener(NoRedirect)
    try:
        response = opener.open(request, timeout=20)
        location = response.headers.get("Location", "")
    except urllib.error.HTTPError as error:
        location = error.headers.get("Location", "")
    if not location.startswith("file://"):
        return None
    parsed = urllib.parse.urlparse(location)
    return Path(urllib.request.url2pathname(parsed.path))


def annotation_groups() -> dict[str, list[dict[str, Any]]]:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for item in all_pages("/items?itemType=annotation"):
        parent = item.get("data", {}).get("parentItem", "")
        if parent:
            grouped.setdefault(parent, []).append(item)
    return grouped


def write_jsonl(path: Path, rows: Iterable[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False, sort_keys=True) + "\n")


def paper_has_fulltext(item: dict[str, Any]) -> bool:
    paper_key = item.get("data", {}).get("key", item.get("key", ""))
    for child in all_pages(f"/items/{urllib.parse.quote(paper_key)}/children"):
        data = child.get("data", {})
        if data.get("itemType") != "attachment" or data.get("contentType") != "application/pdf":
            continue
        _, _, status = get(f"/items/{urllib.parse.quote(child.get('key', ''))}/fulltext")
        if status == 200:
            return True
    return False


def item_key(item: dict[str, Any]) -> str:
    return str(item.get("key") or item.get("data", {}).get("key") or "")


def collection_tree(collection_key: str) -> list[dict[str, Any]]:
    root, _ = get_json(f"/collections/{urllib.parse.quote(collection_key)}")
    pending = [root]
    seen: set[str] = set()
    result: list[dict[str, Any]] = []
    while pending:
        collection = pending.pop(0)
        key = item_key(collection)
        if not key or key in seen:
            continue
        seen.add(key)
        result.append(collection)
        children = all_pages(f"/collections/{urllib.parse.quote(key)}/collections")
        pending.extend(sorted(children, key=item_key))
    return result


def build_revision(
    collection_key: str,
    max_papers: int | None,
    max_chunks_per_paper: int | None,
    require_fulltext: bool = False,
) -> dict[str, Any]:
    start_version = current_library_version()
    collections = collection_tree(collection_key)
    collection_names = {
        item_key(collection): clean(collection.get("data", {}).get("name", ""))
        for collection in collections
    }
    items_by_key: dict[str, dict[str, Any]] = {}
    for collection in collections:
        key = item_key(collection)
        for item in all_pages(f"/collections/{urllib.parse.quote(key)}/items/top"):
            key = item_key(item)
            if key:
                items_by_key.setdefault(key, item)
    candidates = sorted(
        (
            item
            for item in items_by_key.values()
            if item.get("data", {}).get("itemType") not in {"attachment", "annotation", "note"}
        ),
        key=item_key,
    )
    eligible = [item for item in candidates if paper_has_fulltext(item)] if require_fulltext else candidates
    papers = eligible[:max_papers] if max_papers else eligible
    annotations = annotation_groups()
    documents: list[dict[str, Any]] = []
    provenance: dict[str, dict[str, Any]] = {}
    files: list[dict[str, Any]] = []
    warnings: list[dict[str, Any]] = []
    attachment_count = annotation_count = fulltext_count = metadata_count = 0

    for item in papers:
        data = item.get("data", {})
        paper_key = data.get("key", item.get("key", ""))
        paper_collection_keys = sorted(
            key for key in set(data.get("collections", [])) if key in collection_names
        )
        common = {
            "zotero_item_key": paper_key,
            "item_version": data.get("version", item.get("version", 0)),
            "title": clean(data.get("title", "")),
            "creators": creator_names(data),
            "tags": [tag.get("tag", "") for tag in data.get("tags", []) if tag.get("tag")],
            "collection_keys": paper_collection_keys,
            "collection_names": [collection_names[key] for key in paper_collection_keys],
            "zotero_uri": f"zotero://select/library/items/{paper_key}",
        }
        metadata = metadata_text(data)
        if metadata:
            metadata_count += 1
            digest = sha256_bytes(metadata.encode())
            filename = f"{paper_key}-metadata-{digest[:12]}.txt"
            file_source = f"zotero/{paper_key}/metadata/{filename}"
            documents.append(
                {
                    "id": "doc-" + sha256_bytes(f"metadata:{paper_key}:{digest}".encode()),
                    "file_source": file_source,
                    "text": metadata,
                    "sha256": digest,
                    "kind": "metadata",
                }
            )
            provenance[file_source] = {
                **common,
                "attachment_key": None,
                "annotation_key": None,
                "page": None,
                "chunk_sha256": digest,
                "kind": "metadata",
            }
        children = all_pages(f"/items/{urllib.parse.quote(paper_key)}/children")
        for child in children:
            child_data = child.get("data", {})
            if child_data.get("itemType") != "attachment" or child_data.get("contentType") != "application/pdf":
                continue
            attachment_count += 1
            attachment_key = child_data.get("key", child.get("key", ""))
            attachment_path = local_attachment_path(attachment_key)
            if attachment_path and attachment_path.is_file() and not (attachment_path.stat().st_flags & UF_DATALESS):
                files.append(
                    {
                        "attachment_key": attachment_key,
                        "name": attachment_path.name,
                        "size": attachment_path.stat().st_size,
                        "sha256": sha256_file(attachment_path),
                    }
                )
            elif attachment_path and attachment_path.is_file():
                warnings.append({"code": "ATTACHMENT_DATALESS", "attachment_key": attachment_key})
            else:
                warnings.append({"code": "ATTACHMENT_FILE_UNAVAILABLE", "attachment_key": attachment_key})

            for annotation in sorted(annotations.get(attachment_key, []), key=lambda value: value.get("key", "")):
                annotation_data = annotation.get("data", {})
                body = clean(" ".join(filter(None, [annotation_data.get("annotationText"), annotation_data.get("annotationComment")])))
                if not body:
                    continue
                annotation_count += 1
                annotation_key = annotation_data.get("key", annotation.get("key", ""))
                digest = sha256_bytes(body.encode())
                identity = f"annotation:{annotation_key}:{digest}"
                document_id = "doc-" + sha256_bytes(identity.encode())
                page_label = annotation_data.get("annotationPageLabel", "")
                filename = f"{paper_key}-{attachment_key}-annotation-{annotation_key}-page-{page_label or 'unknown'}-{digest[:12]}.txt"
                file_source = f"zotero/{paper_key}/{attachment_key}/{filename}"
                record = {
                    "id": document_id,
                    "file_source": file_source,
                    "text": body,
                    "sha256": digest,
                    "kind": "annotation",
                }
                documents.append(record)
                provenance[file_source] = {
                    **common,
                    "attachment_key": attachment_key,
                    "attachment_version": child_data.get("version", child.get("version", 0)),
                    "annotation_key": annotation_key,
                    "annotation_version": annotation_data.get("version", annotation.get("version", 0)),
                    "page": page_label or None,
                    "chunk_sha256": digest,
                    "kind": "annotation",
                }

            body, _, status = get(f"/items/{urllib.parse.quote(attachment_key)}/fulltext")
            if status >= 400:
                warnings.append({"code": "FULLTEXT_UNAVAILABLE", "attachment_key": attachment_key, "status": status})
                continue
            fulltext_count += 1
            fulltext = json.loads(body)
            for index, (page_index, page_chunk_index, text) in enumerate(page_chunks(fulltext.get("content", ""))):
                if max_chunks_per_paper and index >= max_chunks_per_paper:
                    warnings.append({"code": "FULLTEXT_TRUNCATED", "attachment_key": attachment_key, "limit": max_chunks_per_paper})
                    break
                digest = sha256_bytes(text.encode())
                identity = f"chunk:{attachment_key}:{page_index}:{page_chunk_index}:{digest}"
                document_id = "doc-" + sha256_bytes(identity.encode())
                page = page_index + 1
                filename = f"{paper_key}-{attachment_key}-page-{page:04d}-chunk-{page_chunk_index:03d}-{digest[:12]}.txt"
                file_source = f"zotero/{paper_key}/{attachment_key}/{filename}"
                documents.append({"id": document_id, "file_source": file_source, "text": text, "sha256": digest, "kind": "pdf_chunk"})
                provenance[file_source] = {
                    **common,
                    "attachment_key": attachment_key,
                    "attachment_version": child_data.get("version", child.get("version", 0)),
                    "annotation_key": None,
                    "page": page,
                    "page_label": str(page),
                    "page_chunk_index": page_chunk_index,
                    "chunk_sha256": digest,
                    "kind": "pdf_chunk",
                }

    end_version = current_library_version()
    if start_version != end_version:
        raise SourceChanged(f"SOURCE_CHANGED: Zotero library version changed from {start_version} to {end_version}")

    documents.sort(key=lambda row: row["id"])
    files.sort(key=lambda row: row["attachment_key"])
    return {
        "schema": SCHEMA,
        "publisher_version": PUBLISHER_VERSION,
        "parser": PARSER,
        "chunking": {"characters": CHUNK_SIZE, "overlap_characters": CHUNK_OVERLAP},
        "source": {
            "kind": "zotero-local-api",
            "library_version": start_version,
            "collection_key": collection_key,
            "collection_name": collection_names.get(collection_key, ""),
            "recursive": True,
            "collections": [
                {"key": key, "name": collection_names[key]}
                for key in sorted(collection_names)
            ],
        },
        "counts": {
            "collections": len(collections),
            "papers": len(papers),
            "metadata_documents": metadata_count,
            "attachments": attachment_count,
            "annotations": annotation_count,
            "fulltext_attachments": fulltext_count,
            "documents": len(documents),
        },
        "attachment_files": files,
        "warnings": warnings,
        "documents": documents,
        "provenance": provenance,
    }


def publish(payload: dict[str, Any], output_root: Path) -> Path:
    output_root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".incoming-corpus-", dir=output_root) as temporary:
        staging = Path(temporary)
        documents = payload.pop("documents")
        provenance = payload.pop("provenance")
        write_jsonl(staging / "documents.jsonl", documents)
        (staging / "provenance.json").write_bytes(canonical_json(provenance))
        manifest = {
            **payload,
            "artifacts": {
                "documents.jsonl": {"sha256": sha256_file(staging / "documents.jsonl"), "size": (staging / "documents.jsonl").stat().st_size},
                "provenance.json": {"sha256": sha256_file(staging / "provenance.json"), "size": (staging / "provenance.json").stat().st_size},
            },
        }
        manifest_hash = sha256_bytes(canonical_json(manifest))
        manifest["manifest_sha256"] = manifest_hash
        manifest["corpus_revision"] = "corpus_" + manifest_hash[:20]
        (staging / "manifest.json").write_bytes(canonical_json(manifest))
        destination = output_root / manifest["corpus_revision"]
        if destination.exists():
            existing = json.loads((destination / "manifest.json").read_text())
            if existing.get("manifest_sha256") != manifest_hash:
                raise RuntimeError(f"immutable revision conflict at {destination}")
            return destination
        os.replace(staging, destination)
        return destination


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--collection", required=True)
    parser.add_argument("--output-root", type=Path, required=True)
    parser.add_argument(
        "--max-papers",
        type=int,
        default=0,
        help="optional deterministic cap; 0 (the default) publishes every paper in the collection tree",
    )
    parser.add_argument(
        "--max-chunks-per-paper",
        type=int,
        default=0,
        help="optional per-paper development cap; 0 (the default) preserves complete extracted full text",
    )
    parser.add_argument("--require-fulltext", action="store_true")
    args = parser.parse_args()
    if not 0 <= args.max_papers <= 10_000:
        parser.error("--max-papers must be between 0 and 10000")
    if not 0 <= args.max_chunks_per_paper <= 100_000:
        parser.error("--max-chunks-per-paper must be between 0 and 100000")
    try:
        destination = publish(
            build_revision(
                args.collection,
                args.max_papers or None,
                args.max_chunks_per_paper or None,
                require_fulltext=args.require_fulltext,
            ),
            args.output_root,
        )
    except SourceChanged as error:
        print(json.dumps({"status": "failed", "code": "SOURCE_CHANGED", "message": str(error)}), file=sys.stderr)
        raise SystemExit(3) from error
    manifest = json.loads((destination / "manifest.json").read_text())
    print(json.dumps({"status": "published", "path": str(destination), **manifest["counts"], "corpus_revision": manifest["corpus_revision"], "manifest_sha256": manifest["manifest_sha256"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
