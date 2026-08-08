#!/usr/bin/env python3
"""Query the authenticated ResearchOS literature provenance gateway."""

from __future__ import annotations

import argparse
import json
import os
import urllib.error
import urllib.request
from pathlib import Path


DEFAULT_TOKEN_FILE = Path(__file__).resolve().parents[2] / ".tmp" / "zotero-lightrag.token"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("query")
    parser.add_argument("--endpoint", default=os.getenv("ZOTERO_LIGHTRAG_ENDPOINT", "http://100.90.101.9:8766"))
    parser.add_argument("--token-file", type=Path, default=DEFAULT_TOKEN_FILE)
    parser.add_argument("--backend", choices=("paperqa2",), default="paperqa2")
    args = parser.parse_args()
    token = args.token_file.read_text().strip()
    request = urllib.request.Request(
        args.endpoint.rstrip("/") + "/query",
        data=json.dumps(
            {
                "query": args.query,
                "backend": args.backend,
                "enable_rerank": True,
            }
        ).encode(),
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=300) as response:
            payload = json.load(response)
    except urllib.error.HTTPError as error:
        try:
            payload = json.loads(error.read())
        except (json.JSONDecodeError, UnicodeDecodeError):
            payload = {"status": "failed", "code": "GATEWAY_HTTP_ERROR"}
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        raise SystemExit(1) from error
    print(json.dumps(payload, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
