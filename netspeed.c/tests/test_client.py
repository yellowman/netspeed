#!/usr/bin/env python3
"""Process-level protocol-v2 checks for the native C client."""

from __future__ import annotations

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import subprocess
import sys
import threading
import time
from urllib.parse import parse_qs, urlsplit

TOKEN = "c-test-token"
MAX_TRANSFER = 4 * 1024 * 1024
CHUNK = 64 * 1024


class ProtocolHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "netspeed-c-test"

    def log_message(self, _format: str, *_args: object) -> None:
        return

    def _authorized(self) -> bool:
        if self.headers.get("Authorization") == f"Bearer {TOKEN}":
            return True
        self.send_response(401)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "private, no-store")
        self.send_header("Content-Length", "25")
        self.end_headers()
        self.wfile.write(b'{"error":"unauthorized"}')
        return False

    def _json(self, value: object, *, cache_control: str = "private, no-store") -> None:
        body = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", cache_control)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if not self._authorized():
            return
        parsed = urlsplit(self.path)
        mode = getattr(self.server, "mode", "normal")
        if parsed.path == "/meta":
            self._json(
                {
                    "hostname": "c-fixture",
                    "clientIp": "127.0.0.1",
                    "httpProtocol": "HTTP/1.1",
                    "asn": 0,
                    "asOrganization": "",
                    "colo": "local",
                    "country": "US",
                    "city": "",
                    "region": "",
                    "postalCode": "",
                    "latitude": 0,
                    "longitude": 0,
                    "maxTransferBytes": MAX_TRANSFER,
                    "maxConcurrentTransfersPerClient": 8,
                    "measurementProtocolVersion": 1 if mode == "old-protocol" else 2,
                    "uploadReceiptVersion": 1,
                    "packetLossFrameVersion": 1,
                }
            )
            return
        if parsed.path != "/__down":
            self.send_error(404)
            return
        try:
            size = int(parse_qs(parsed.query).get("bytes", ["-1"])[0])
        except ValueError:
            self.send_error(400)
            return
        if size < 0 or size > MAX_TRANSFER:
            self.send_error(413)
            return
        response_size = size - 1 if mode == "truncated-download" and size > 0 else size
        # Give request-to-first-byte a measurable, non-zero interval.
        time.sleep(0.003)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Cache-Control", "no-store, no-transform")
        self.send_header("Content-Length", str(response_size))
        self.end_headers()
        remaining = response_size
        block = b"\0" * CHUNK
        try:
            while remaining:
                amount = min(remaining, len(block))
                self.wfile.write(block[:amount])
                self.wfile.flush()
                remaining -= amount
                if remaining:
                    time.sleep(0.002)
        except (BrokenPipeError, ConnectionResetError):
            return

    def do_POST(self) -> None:  # noqa: N802
        if not self._authorized():
            return
        parsed = urlsplit(self.path)
        mode = getattr(self.server, "mode", "normal")
        if parsed.path != "/__up":
            self.send_error(404)
            return
        try:
            size = int(self.headers.get("Content-Length", "-1"))
        except ValueError:
            self.send_error(400)
            return
        if size < 0 or size > MAX_TRANSFER:
            self.send_error(413)
            return
        started = time.monotonic_ns()
        remaining = size
        while remaining:
            data = self.rfile.read(min(CHUNK, remaining))
            if not data:
                self.send_error(400)
                return
            remaining -= len(data)
            if remaining:
                time.sleep(0.002)
        duration = max(1, time.monotonic_ns() - started)
        self._json(
            {
                "ok": True,
                "receiptVersion": 1,
                "acceptedBytes": size - 1 if mode == "bad-upload-receipt" and size > 0 else size,
                "serverDurationNs": duration,
            }
        )


def run_client(binary: Path, server_url: str, direction: str, label: str = "client") -> dict[str, object]:
    flag = "--download-only" if direction == "download" else "--upload-only"
    command = [
        str(binary),
        "--server",
        server_url,
        "--token",
        TOKEN,
        "--quick",
        flag,
        "--no-packet-loss",
        "--json",
        "--timeout",
        "30s",
    ]
    completed = subprocess.run(command, text=True, capture_output=True, timeout=45)
    if completed.returncode != 0:
        raise AssertionError(
            f"{label} {direction} run failed ({completed.returncode})\n"
            f"stdout:\n{completed.stdout}\nstderr:\n{completed.stderr}"
        )
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise AssertionError(f"{label} emitted invalid JSON: {completed.stdout!r}") from exc
    return payload


def run_expected_failure(
    binary: Path,
    server_url: str,
    direction: str,
) -> dict[str, object]:
    flag = "--download-only" if direction == "download" else "--upload-only"
    completed = subprocess.run(
        [
            str(binary),
            "--server",
            server_url,
            "--token",
            TOKEN,
            "--quick",
            flag,
            "--no-packet-loss",
            "--json",
            "--timeout",
            "10s",
        ],
        text=True,
        capture_output=True,
        timeout=20,
    )
    if completed.returncode == 0:
        raise AssertionError(f"C client unexpectedly accepted an invalid {direction} contract")
    try:
        payload = json.loads(completed.stderr.strip())
    except json.JSONDecodeError as exc:
        raise AssertionError(
            f"C client failure was not valid JSON: {completed.stderr!r}"
        ) from exc
    assert isinstance(payload.get("error"), str) and payload["error"]
    return payload


def validate(payload: dict[str, object], direction: str) -> None:
    meta = payload["meta"]
    assert isinstance(meta, dict)
    assert meta["measurementProtocolVersion"] == 2
    assert meta["uploadReceiptVersion"] == 1
    assert meta["packetLossFrameVersion"] == 1
    assert payload["packetLoss"] is None
    summary = payload["summary"]
    assert isinstance(summary, dict)
    assert summary[f"{direction}Mbps"] > 0
    assert summary["packetLossPercent"] is None

    throughput = payload["throughputSamples"]
    assert isinstance(throughput, list)
    directional = [sample for sample in throughput if sample["direction"] == direction]
    baselines = [sample for sample in directional if sample.get("sampleKind") == "baseline"]
    windows = [sample for sample in directional if sample.get("sampleKind") == "window"]
    assert len(baselines) == 6, baselines
    assert len(windows) == 1, windows
    assert windows[0]["requestCount"] > 0
    assert windows[0]["concurrency"] >= 1
    assert windows[0]["timingSource"] == "aggregate-wall-clock"

    latency = payload["latencySamples"]
    assert isinstance(latency, list)
    unloaded = [sample for sample in latency if sample["condition"] == "unloaded"]
    loaded = [sample for sample in latency if sample["condition"] == direction]
    assert len(unloaded) >= 3
    assert len(loaded) >= 2, loaded
    assert all(sample.get("loadOverlapped") is True for sample in loaded)
    assert all(sample.get("loadTrackingAccurate") is True for sample in loaded)
    assert all(sample["startedAt"] <= sample["endedAt"] for sample in loaded)

    confidence = payload["testConfidence"]
    assert isinstance(confidence, dict)
    assert confidence["metrics"]["loadedOverlap"][f"{direction}Accepted"] >= 2
    assert any("packet" in warning.lower() for warning in confidence["warnings"])


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: test_client.py PATH-TO-NETSPEED-C")
    binary = Path(sys.argv[1]).resolve()
    server = ThreadingHTTPServer(("127.0.0.1", 0), ProtocolHandler)
    server.mode = "normal"  # type: ignore[attr-defined]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        url = f"http://127.0.0.1:{server.server_port}"
        validate(run_client(binary, url, "download"), "download")
        validate(run_client(binary, url, "upload"), "upload")
        server.mode = "old-protocol"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "truncated-download"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "bad-upload-receipt"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "upload")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
    print("C protocol-v2 process tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
