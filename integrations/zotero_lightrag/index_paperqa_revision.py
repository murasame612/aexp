#!/usr/bin/env python3
"""Build a deterministic PaperQA2 derived index for one frozen corpus."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

try:
    from .paperqa_backend import build_index
except ImportError:  # Direct script execution on the deployed host.
    from paperqa_backend import build_index


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("revision", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--embedding-mode", choices=("sparse", "hybrid"), default="hybrid")
    args = parser.parse_args()
    result = build_index(args.revision, args.output, embedding_mode=args.embedding_mode)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
