#!/usr/bin/env python3
from __future__ import annotations

import argparse
import os
from http import HTTPStatus
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote, urlparse


class SpaStaticHandler(SimpleHTTPRequestHandler):
    def __init__(self, *args, directory: str, **kwargs):
        self._root = Path(directory).resolve()
        super().__init__(*args, directory=directory, **kwargs)

    def end_headers(self) -> None:
        path = unquote(urlparse(self.path).path or "/")
        if path in {"/", "/index.html"}:
            self.send_header("Cache-Control", "no-store")
        elif path == "/service-worker.js":
            self.send_header("Cache-Control", "no-cache, must-revalidate")
        elif path.startswith("/assets/"):
            self.send_header("Cache-Control", "public, max-age=31536000, immutable")
        super().end_headers()

    def do_GET(self) -> None:  # noqa: N802
        self._serve_request(head_only=False)

    def do_HEAD(self) -> None:  # noqa: N802
        self._serve_request(head_only=True)

    def _serve_request(self, head_only: bool) -> None:
        parsed = urlparse(self.path)
        request_path = unquote(parsed.path or "/")
        if request_path.startswith("/api"):
            self.send_error(HTTPStatus.NOT_FOUND, "API routes are served by the backend proxy")
            return

        translated = Path(self.translate_path(request_path)).resolve()
        if translated.is_file():
            if head_only:
                self._serve_existing_head(request_path)
            else:
                super().do_GET()
            return

        index_file = self._root / "index.html"
        if not index_file.is_file():
            self.send_error(HTTPStatus.NOT_FOUND, "Missing index.html in frontend dist directory")
            return

        self.path = "/index.html"
        if head_only:
            self._serve_existing_head("/index.html")
        else:
            super().do_GET()

    def _serve_existing_head(self, request_path: str) -> None:
        translated = self.translate_path(request_path)
        with open(translated, "rb") as source:
            info = os.fstat(source.fileno())
            self.send_response(HTTPStatus.OK)
            self.send_header("Content-type", self.guess_type(translated))
            self.send_header("Content-Length", str(info.st_size))
            self.send_header("Last-Modified", self.date_time_string(info.st_mtime))
            self.end_headers()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Serve ActRail frontend build output with SPA fallback.")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=18743)
    parser.add_argument("--dir", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    root = Path(args.dir).resolve()
    if not root.is_dir():
        raise SystemExit(f"frontend dist directory not found: {root}")

    handler = lambda *handler_args, **handler_kwargs: SpaStaticHandler(*handler_args, directory=str(root), **handler_kwargs)
    server = ThreadingHTTPServer((args.host, args.port), handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
