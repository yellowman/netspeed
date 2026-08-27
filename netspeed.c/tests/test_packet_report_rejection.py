#!/usr/bin/env python3
"""Serve HTTP-200 ok:false packet counters and require the C client to reject them."""

from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import subprocess
import sys
import threading

TOKEN = "packet-report-token"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def _authorized(self) -> bool:
        return self.headers.get("Authorization") == f"Bearer {TOKEN}"

    def _json(self, payload: object, status: int = 200) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if not self._authorized():
            self._json({"error": "unauthorized"}, 401)
            return
        if self.path == "/api/turn/credentials":
            self._json(
                {
                    "username": "fixture-user",
                    "credential": "fixture-password",
                    "servers": ["turn:127.0.0.1:3478?transport=udp"],
                    "ttlSec": 60,
                }
            )
            return
        self._json({"error": "not found"}, 404)

    def do_POST(self) -> None:  # noqa: N802
        if not self._authorized():
            self._json({"error": "unauthorized"}, 401)
            return
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        payload = json.loads(body or b"{}")
        if self.path == "/api/packet-test/offer":
            self._json({"sdp": "v=0\r\na=fake-answer\r\n", "type": "answer", "testId": "rejected-report"})
            return
        if self.path == "/api/packet-test/report":
            sent = int(payload.get("sent", 0))
            received = int(payload.get("received", 0))
            # All numeric counters are internally consistent. Only ok:false
            # distinguishes this authoritative rejection from a valid result.
            self._json(
                {
                    "ok": False,
                    "protocolVersion": 2,
                    "frameSizeBytes": 1200,
                    "forwardReceived": sent,
                    "acknowledgementsSent": sent,
                    "duplicateFrames": 0,
                    "invalidFrames": 0,
                    "ackSendFailures": 0,
                    "clientAcknowledgementsReceived": received,
                }
            )
            return
        self._json({"error": "not found"}, 404)


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: test_packet_report_rejection.py TEST-BINARY")
    binary = Path(sys.argv[1]).resolve()
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        completed = subprocess.run(
            [str(binary), f"http://127.0.0.1:{server.server_port}", TOKEN],
            text=True,
            capture_output=True,
            timeout=20,
        )
        if completed.returncode != 0:
            raise AssertionError(
                f"packet-report rejection fixture failed ({completed.returncode})\n"
                f"stdout:\n{completed.stdout}\nstderr:\n{completed.stderr}"
            )
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
    print("HTTP-200 ok:false packet-report fixture passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
