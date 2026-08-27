#!/usr/bin/env python3
"""Process-level checks for the native C Cloudflare provider adapter."""

from __future__ import annotations

import argparse
import http.server
import json
import socketserver
import subprocess
import threading
import urllib.parse


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args: object) -> None:
        return

    def do_GET(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.path == "/meta":
            body = b"not a netspeed meta document"
            self.send_response(404)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if parsed.path == "/__down":
            size = int(urllib.parse.parse_qs(parsed.query).get("bytes", ["0"])[0])
            self.send_response(200)
            self.send_header("CF-Ray", "fixture")
            self.send_header("Server", "cloudflare")
            self.send_header("Server-Timing", "cfReqDur;dur=0.1")
            self.send_header("Content-Length", str(size))
            self.end_headers()
            block = b"0" * 65536
            while size:
                amount = min(size, len(block))
                self.wfile.write(block[:amount])
                size -= amount
            return
        self.send_response(404)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlsplit(self.path)
        size = int(self.headers.get("Content-Length", "0"))
        received = 0
        while received < size:
            data = self.rfile.read(min(65536, size - received))
            if not data:
                break
            received += len(data)
        if parsed.path != "/__up" or received != size:
            self.send_response(400)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        body = b"ok"
        self.send_response(200)
        self.send_header("CF-Ray", "fixture")
        self.send_header("Server", "cloudflare")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def invoke(binary: str, args: list[str]) -> dict[str, object]:
    completed = subprocess.run(
        [binary, *args], text=True, capture_output=True, timeout=45
    )
    if completed.returncode:
        raise AssertionError(
            f"command failed ({completed.returncode}): {args!r}\n"
            f"stdout={completed.stdout}\nstderr={completed.stderr}"
        )
    payload = json.loads(completed.stdout)
    assert payload["provider"] == "cloudflare", payload
    assert payload["measurementContract"] == "cloudflare-http-v1", payload
    assert payload["packetTopology"] == "turn-loopback", payload
    return payload


def run(binary: str) -> None:
    server = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{server.server_address[1]}"
    try:
        # Explicit provider mode accepts a positional server URL, the standard
        # -q quick shorthand, and the canonical packet-skip flag.
        download = invoke(
            binary,
            [
                "--provider",
                "cloudflare",
                "-q",
                "--download-only",
                "--no-packet-loss",
                "--json",
                base,
            ],
        )
        assert download["download"]["available"] is True
        assert download["upload"]["available"] is False

        # Auto mode is selected only because the response carries a positive
        # Cloudflare fingerprint. The compatibility packet-skip alias remains
        # accepted for existing scripts.
        upload = invoke(
            binary,
            [
                "--provider=auto",
                "--server",
                base,
                "--quick",
                "--upload-only",
                "--skip-packet-loss",
                "--json",
            ],
        )
        assert upload["upload"]["available"] is True
        assert upload["upload"]["evidence"] == "client-observed-complete-body"
        assert upload["download"]["available"] is False

        bad = subprocess.run(
            [binary, "--provider", "other", "--server", base],
            text=True,
            capture_output=True,
            timeout=10,
        )
        assert bad.returncode == 2, bad
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    run(parser.parse_args().binary)
    print("C Cloudflare provider process tests passed")
