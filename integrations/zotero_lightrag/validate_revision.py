#!/usr/bin/env python3
"""Validate the immutable Zotero corpus revision contract."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path


def canonical_json(value: object) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def validate(root: Path) -> dict[str, object]:
    manifest = json.loads((root / "manifest.json").read_text())
    expected_hash = manifest.pop("manifest_sha256")
    revision = manifest.pop("corpus_revision")
    actual_hash = hashlib.sha256(canonical_json(manifest)).hexdigest()
    if expected_hash != actual_hash:
        raise ValueError(f"manifest hash mismatch: expected {expected_hash}, got {actual_hash}")
    if revision != "corpus_" + actual_hash[:20]:
        raise ValueError("corpus revision does not match manifest hash")
    for name, expected in manifest.get("artifacts", {}).items():
        path = root / name
        if not path.is_file():
            raise ValueError(f"missing artifact: {name}")
        if path.stat().st_size != expected["size"]:
            raise ValueError(f"size mismatch: {name}")
        if sha256_file(path) != expected["sha256"]:
            raise ValueError(f"sha256 mismatch: {name}")
    documents = [json.loads(line) for line in (root / "documents.jsonl").read_text().splitlines() if line]
    provenance = json.loads((root / "provenance.json").read_text())
    if len(documents) != manifest["counts"]["documents"]:
        raise ValueError("document count does not match manifest")
    missing = sorted({row["file_source"] for row in documents} - set(provenance))
    if missing:
        raise ValueError(f"documents missing provenance: {missing[:3]}")
    basenames = [Path(row["file_source"]).name for row in documents]
    if len(basenames) != len(set(basenames)):
        raise ValueError("file_source basenames must be globally unique for provenance resolution")
    return {"status": "verified", "corpus_revision": revision, "manifest_sha256": expected_hash, "documents": len(documents), "provenance": len(provenance)}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("revision", type=Path)
    args = parser.parse_args()
    print(json.dumps(validate(args.revision), ensure_ascii=False))


if __name__ == "__main__":
    main()
