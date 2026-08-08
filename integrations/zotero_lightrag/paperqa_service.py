#!/usr/bin/env python3
"""Private HTTP service exposing PaperQA2 behind the provenance gateway."""

from __future__ import annotations

import argparse
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

try:
    from .paperqa_backend import PaperQARuntime
except ImportError:  # Direct script execution on the deployed host.
    from paperqa_backend import PaperQARuntime


def handler_for(runtime: PaperQARuntime, api_key: str):
    class Handler(BaseHTTPRequestHandler):
        server_version = "ResearchOS-PaperQA2/1"

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
            return hmac.compare_digest(self.headers.get("X-API-Key", ""), api_key)

        def do_GET(self) -> None:
            if not self.authorized():
                self.send_json(401, {"detail": "unauthorized"})
                return
            if self.path != "/health":
                self.send_json(404, {"detail": "not found"})
                return
            self.send_json(200, runtime.active())

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
                if len(str(body.get("query", "")).strip()) < 3:
                    self.send_json(400, {"detail": "query too short"})
                    return
                self.send_json(200, runtime.query(body))
            except RuntimeError as error:
                self.send_json(503, {"detail": str(error)})
            except (ValueError, json.JSONDecodeError) as error:
                self.send_json(400, {"detail": str(error)})

    return Handler


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default=os.getenv("PAPERQA_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, default=int(os.getenv("PAPERQA_PORT", "9631")))
    parser.add_argument("--state-root", type=Path, default=Path(os.getenv("PAPERQA_STATE_ROOT", ".")))
    args = parser.parse_args()
    runtime = PaperQARuntime(args.state_root)
    ThreadingHTTPServer(
        (args.host, args.port),
        handler_for(runtime, os.environ["PAPERQA_API_KEY"]),
    ).serve_forever()


if __name__ == "__main__":
    main()
