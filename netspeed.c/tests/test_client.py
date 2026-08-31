#!/usr/bin/env python3
"""Process-level HTTP transport checks for the native C Netspeed client."""

from __future__ import annotations

import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import struct
import subprocess
import sys
import threading
import time
from urllib.parse import parse_qs, urlsplit

TOKEN = "c-test-token"
MAX_TRANSFER = 4 * 1024 * 1024
DEFAULT_CHUNK = 64 * 1024
MIN_CHUNK = 4 * 1024
MAX_CHUNK = 1024 * 1024
DOWNLOAD_PATH = "/measure/down"
UPLOAD_PATH = "/measure/up"
PING_PATH = "/measure/ping"
WEBSOCKET_PING_PATH = "/measure/ws"
WEBSOCKET_PING_PROTOCOL = "netspeed.ping.v1"
WEBSOCKET_PING_PAYLOAD_BYTES = 16
WEBSOCKET_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

# Deterministic, non-zero-fill bytes. The native client verifies the advertised
# discriminator and exact length; the fixture keeps the payload stable so a
# contract failure is reproducible.
RANDOM_BLOCK = bytes(((index * 73 + 41) ^ (index >> 3)) & 0xFF for index in range(DEFAULT_CHUNK))


def capabilities() -> dict[str, object]:
    return {
        "version": 1,
        "downloadPath": DOWNLOAD_PATH,
        "downloadBytesParameter": "n",
        "downloadPayloadParameter": "kind",
        "downloadFramingParameter": "wire",
        "downloadChunkBytesParameter": "block",
        "downloadFlushParameter": "emit",
        "uploadPath": UPLOAD_PATH,
        "uploadBytesParameter": "expected",
        "httpPingPath": PING_PATH,
        "httpPingMethods": ["GET", "HEAD"],
        "webSocketPingPath": WEBSOCKET_PING_PATH,
        "webSocketPingProtocol": WEBSOCKET_PING_PROTOCOL,
        "webSocketPingPayloadBytes": WEBSOCKET_PING_PAYLOAD_BYTES,
        "warmConnectionPing": True,
        "downloadPayloads": ["random", "zero"],
        "downloadFramings": ["fixed", "chunked"],
        "defaultDownloadPayload": "random",
        "defaultDownloadFraming": "fixed",
        "defaultChunkBytes": DEFAULT_CHUNK,
        "minimumChunkBytes": MIN_CHUNK,
        "maximumChunkBytes": MAX_CHUNK,
        "uploadContentEncodings": ["identity"],
        "responseCacheControl": "no-store, no-transform",
        "noTransform": True,
        "proxyBufferSuppressionHeader": "X-Accel-Buffering: no",
        "proxyRequestBufferingAdvisory": True,
    }


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

    def _json(
        self,
        value: object,
        *,
        cache_control: str = "private, no-store",
        extra_headers: dict[str, str] | None = None,
    ) -> None:
        body = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", cache_control)
        if extra_headers:
            for name, header_value in extra_headers.items():
                self.send_header(name, header_value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _measurement_request_valid(self, *, upload: bool = False) -> bool:
        cache_control = self.headers.get("Cache-Control", "").lower()
        valid = (
            self.headers.get("Accept-Encoding", "").lower() == "identity"
            and "no-store" in cache_control
            and "no-transform" in cache_control
            and self.headers.get("Pragma", "").lower() == "no-cache"
        )
        if upload:
            valid = valid and self.headers.get("Content-Encoding", "").lower() == "identity"
        if valid:
            return True
        body = b'{"error":"measurement request controls missing"}'
        self.send_response(400)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)
        self.close_connection = True
        return False

    def _measurement_headers(self, measurement: str, mode: str) -> None:
        if mode == "missing-no-transform":
            self.send_header("Cache-Control", "no-store")
        elif mode == "split-cache-control":
            self.send_header("Cache-Control", "no-store")
            self.send_header("Cache-Control", "no-transform")
        else:
            self.send_header("Cache-Control", "no-store, no-transform")
        if mode != "missing-proxy-suppression":
            self.send_header("X-Accel-Buffering", "no")
        self.send_header("X-Netspeed-Measurement", measurement)

    def _serve_ping(self, mode: str, *, head: bool = False) -> None:
        if not self._measurement_request_valid():
            return
        time.sleep(0.003)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self._measurement_headers("latency", mode)
        self.send_header("Content-Length", "0")
        if mode == "cold-latency":
            self.send_header("Connection", "close")
            self.close_connection = True
        self.end_headers()
        if not head:
            self.wfile.flush()

    def _serve_download(self, parsed: object, mode: str) -> None:
        if not self._measurement_request_valid():
            return
        query = parse_qs(parsed.query)  # type: ignore[attr-defined]
        try:
            size = int(query.get("n", ["-1"])[0])
            chunk_bytes = int(query.get("block", ["-1"])[0])
        except ValueError:
            self.send_error(400)
            return
        payload = query.get("kind", [""])[0]
        framing = query.get("wire", [""])[0]
        flush = query.get("emit", [""])[0]
        if (
            size < 0
            or size > MAX_TRANSFER
            or payload not in {"random", "zero"}
            or framing not in {"fixed", "chunked"}
            or chunk_bytes < MIN_CHUNK
            or chunk_bytes > MAX_CHUNK
            or flush not in {"true", "false"}
        ):
            self.send_error(400)
            return

        response_size = size - 1 if mode == "truncated-download" and size > 0 else size
        reported_payload = "zero" if mode == "payload-mismatch" and payload == "random" else payload
        reported_framing = (
            "chunked" if mode == "framing-mismatch" and framing == "fixed" else framing
        )
        reported_chunk_bytes = chunk_bytes + 1 if mode == "chunk-mismatch" else chunk_bytes
        reported_flush = (
            "false" if flush == "true" else "true"
        ) if mode == "flush-mismatch" else flush
        time.sleep(0.003)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self._measurement_headers("download", mode)
        self.send_header("X-Netspeed-Payload", reported_payload)
        self.send_header("X-Netspeed-Framing", reported_framing)
        self.send_header("X-Netspeed-Chunk-Bytes", str(reported_chunk_bytes))
        self.send_header("X-Netspeed-Flush", reported_flush)
        if mode == "encoded-download":
            self.send_header("Content-Encoding", "gzip")
        elif mode == "stacked-encoding":
            self.send_header("Content-Encoding", "identity")
            self.send_header("Content-Encoding", "gzip")
        if framing == "fixed":
            self.send_header("Content-Length", str(response_size))
        else:
            self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()

        source = b"\0" * DEFAULT_CHUNK if payload == "zero" else RANDOM_BLOCK
        remaining = response_size
        try:
            while remaining:
                amount = min(remaining, chunk_bytes)
                # Repeat only inside this deterministic test fixture. The real daemon
                # emits a per-request pseudorandom stream.
                data = (source * ((amount + len(source) - 1) // len(source)))[:amount]
                if framing == "chunked":
                    self.wfile.write(f"{amount:x}\r\n".encode())
                    self.wfile.write(data)
                    self.wfile.write(b"\r\n")
                else:
                    self.wfile.write(data)
                if flush == "true":
                    self.wfile.flush()
                remaining -= amount
                if remaining:
                    time.sleep(0.001)
            if framing == "chunked":
                self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            return

    def _read_exact(self, length: int) -> bytes:
        result = bytearray()
        while len(result) < length:
            chunk = self.connection.recv(length - len(result))
            if not chunk:
                raise ConnectionError("WebSocket peer closed")
            result.extend(chunk)
        return bytes(result)

    def _read_websocket_frame(self) -> tuple[int, bytes]:
        header = self._read_exact(2)
        opcode = header[0] & 0x0F
        masked = bool(header[1] & 0x80)
        length = header[1] & 0x7F
        if length == 126:
            length = struct.unpack("!H", self._read_exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self._read_exact(8))[0]
        if not masked:
            raise ValueError("client WebSocket frame was not masked")
        mask = self._read_exact(4)
        payload = bytearray(self._read_exact(length))
        for index in range(length):
            payload[index] ^= mask[index % 4]
        return opcode, bytes(payload)

    def _send_websocket_frame(self, opcode: int, payload: bytes) -> None:
        first = 0x80 | opcode
        length = len(payload)
        if length < 126:
            header = bytes((first, length))
        elif length <= 0xFFFF:
            header = bytes((first, 126)) + struct.pack("!H", length)
        else:
            header = bytes((first, 127)) + struct.pack("!Q", length)
        self.connection.sendall(header + payload)

    def _serve_websocket_ping(self, mode: str) -> None:
        upgrade = self.headers.get("Upgrade", "").lower()
        connection = self.headers.get("Connection", "").lower()
        key = self.headers.get("Sec-WebSocket-Key", "")
        protocol = self.headers.get("Sec-WebSocket-Protocol", "")
        if (
            upgrade != "websocket"
            or "upgrade" not in {token.strip() for token in connection.split(",")}
            or self.headers.get("Sec-WebSocket-Version") != "13"
            or not key
            or protocol != WEBSOCKET_PING_PROTOCOL
            or not self._measurement_request_valid()
        ):
            return
        with self.server.websocket_lock:  # type: ignore[attr-defined]
            self.server.websocket_upgrade_attempts += 1  # type: ignore[attr-defined]
        if mode == "websocket-reject":
            body = b'{"error":"websocket disabled"}'
            self.send_response(503)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(body)
            self.close_connection = True
            return

        accept = base64.b64encode(
            hashlib.sha1((key + WEBSOCKET_GUID).encode("ascii")).digest()
        ).decode("ascii")
        self.send_response(101, "Switching Protocols")
        self.send_header("Upgrade", "websocket")
        self.send_header("Connection", "Upgrade")
        self.send_header("Sec-WebSocket-Accept", accept)
        self.send_header("Sec-WebSocket-Protocol", WEBSOCKET_PING_PROTOCOL)
        self.send_header("Cache-Control", "no-store, no-transform")
        self.send_header("Pragma", "no-cache")
        self.send_header("X-Accel-Buffering", "no")
        if mode == "websocket-stacked-encoding":
            self.send_header("Content-Encoding", "identity")
            self.send_header("Content-Encoding", "gzip")
        self.send_header("X-Netspeed-Measurement", "latency")
        self.end_headers()
        self.wfile.flush()
        self.connection.settimeout(15)
        with self.server.websocket_lock:  # type: ignore[attr-defined]
            self.server.websocket_connections += 1  # type: ignore[attr-defined]
        try:
            while True:
                opcode, payload = self._read_websocket_frame()
                if opcode == 0x8:
                    self._send_websocket_frame(0x8, payload[:125])
                    break
                if opcode == 0x9:
                    self._send_websocket_frame(0xA, payload[:125])
                    continue
                if opcode != 0x2 or len(payload) != WEBSOCKET_PING_PAYLOAD_BYTES:
                    self._send_websocket_frame(0x8, b"\x03\xea")
                    break
                if mode == "websocket-bad-echo":
                    payload = payload[:-1] + bytes((payload[-1] ^ 0xFF,))
                self._send_websocket_frame(0x2, payload)
                with self.server.websocket_lock:  # type: ignore[attr-defined]
                    self.server.websocket_messages += 1  # type: ignore[attr-defined]
        except (BrokenPipeError, ConnectionError, ConnectionResetError, socket.timeout):
            pass
        finally:
            self.close_connection = True

    def do_HEAD(self) -> None:  # noqa: N802
        if not self._authorized():
            return
        parsed = urlsplit(self.path)
        mode = getattr(self.server, "mode", "normal")
        if parsed.path == PING_PATH:
            self._serve_ping(mode, head=True)
            return
        self.send_error(404)

    def do_GET(self) -> None:  # noqa: N802
        if not self._authorized():
            return
        parsed = urlsplit(self.path)
        mode = getattr(self.server, "mode", "normal")
        if parsed.path == "/meta":
            document: dict[str, object] = {
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
            if mode != "legacy":
                advertised = capabilities()
                if mode == "head-only-ping":
                    advertised["httpPingMethods"] = ["HEAD"]
                    advertised.pop("webSocketPingPath", None)
                    advertised.pop("webSocketPingProtocol", None)
                    advertised.pop("webSocketPingPayloadBytes", None)
                elif mode == "cold-latency":
                    advertised.pop("webSocketPingPath", None)
                    advertised.pop("webSocketPingProtocol", None)
                    advertised.pop("webSocketPingPayloadBytes", None)
                document["measurementCapabilities"] = advertised
            self._json(document)
            return
        if parsed.path == WEBSOCKET_PING_PATH:
            self._serve_websocket_ping(mode)
            return
        if parsed.path == PING_PATH:
            self._serve_ping(mode)
            return
        if parsed.path == DOWNLOAD_PATH:
            self._serve_download(parsed, mode)
            return
        if mode == "legacy" and parsed.path == "/__down":
            if not self._measurement_request_valid():
                return
            try:
                size = int(parse_qs(parsed.query).get("bytes", ["-1"])[0])
            except ValueError:
                self.send_error(400)
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Cache-Control", "no-store, no-transform")
            self.send_header("Content-Length", str(size))
            self.end_headers()
            remaining = size
            while remaining:
                amount = min(DEFAULT_CHUNK, remaining)
                self.wfile.write(b"\0" * amount)
                remaining -= amount
                if remaining:
                    self.wfile.flush()
                    time.sleep(0.001)
            return
        self.send_error(404)

    def do_POST(self) -> None:  # noqa: N802
        if not self._authorized():
            return
        parsed = urlsplit(self.path)
        mode = getattr(self.server, "mode", "normal")
        legacy = mode == "legacy"
        if parsed.path != ("/__up" if legacy else UPLOAD_PATH):
            self.send_error(404)
            return
        if not self._measurement_request_valid(upload=True):
            return
        query = parse_qs(parsed.query)
        try:
            size = int(self.headers.get("Content-Length", "-1"))
            expected = size if legacy else int(query.get("expected", ["-1"])[0])
        except ValueError:
            self.send_error(400)
            return
        if size < 0 or size > MAX_TRANSFER or expected != size:
            self.send_error(400)
            return
        started = time.monotonic_ns()
        remaining = size
        while remaining:
            data = self.rfile.read(min(DEFAULT_CHUNK, remaining))
            if not data:
                self.send_error(400)
                return
            remaining -= len(data)
            if remaining:
                time.sleep(0.001)
        duration = max(1, time.monotonic_ns() - started)
        accepted = size - 1 if mode == "bad-upload-receipt" and size > 0 else size
        headers: dict[str, str] = {}
        if not legacy:
            headers = {
                "X-Accel-Buffering": "no",
                "X-Netspeed-Measurement": "upload",
                "X-Netspeed-Payload": "discarded",
                "X-Netspeed-Framing": "fixed",
                "X-Netspeed-Content-Encoding": "identity",
                "X-Netspeed-Expected-Bytes": str(size),
                "X-Netspeed-Accepted-Bytes": str(
                    size - 1 if mode == "bad-upload-header" and size > 0 else size
                ),
                "X-Netspeed-Upload-Duration-Ns": str(duration),
            }
        self._json(
            {
                "ok": True,
                "receiptVersion": 1,
                "acceptedBytes": accepted,
                "serverDurationNs": duration,
            },
            cache_control="no-store, no-transform" if not legacy else "private, no-store",
            extra_headers=headers,
        )


def command_for(
    binary: Path,
    server_url: str,
    direction: str,
    provider: str | None,
    extra_args: list[str] | None = None,
) -> list[str]:
    flag = "--download-only" if direction == "download" else "--upload-only"
    command = [str(binary)]
    if provider is not None:
        command.extend(["--provider", provider])
    command.extend(
        [
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
    )
    if extra_args:
        command.extend(extra_args)
    return command


def run_client(
    binary: Path,
    server_url: str,
    direction: str,
    label: str = "client",
    provider: str | None = None,
    extra_args: list[str] | None = None,
) -> dict[str, object]:
    command = command_for(binary, server_url, direction, provider, extra_args)
    completed = subprocess.run(command, text=True, capture_output=True, timeout=45)
    if completed.returncode != 0:
        raise AssertionError(
            f"{label} {direction} run failed ({completed.returncode})\n"
            f"command: {command!r}\nstdout:\n{completed.stdout}\nstderr:\n{completed.stderr}"
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
    provider: str | None = None,
    extra_args: list[str] | None = None,
    expected_status: int | None = None,
) -> dict[str, object]:
    command = command_for(binary, server_url, direction, provider, extra_args)
    completed = subprocess.run(command, text=True, capture_output=True, timeout=20)
    if completed.returncode == 0:
        raise AssertionError(f"C client unexpectedly accepted an invalid {direction} contract")
    if expected_status is not None:
        assert completed.returncode == expected_status, completed
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

    advertised = meta["measurementCapabilities"]
    assert isinstance(advertised, dict)
    assert advertised["version"] == 1
    assert advertised["downloadPath"] == DOWNLOAD_PATH
    assert advertised["uploadPath"] == UPLOAD_PATH
    assert advertised["httpPingPath"] == PING_PATH
    assert advertised["webSocketPingPath"] == WEBSOCKET_PING_PATH
    assert advertised["webSocketPingProtocol"] == WEBSOCKET_PING_PROTOCOL
    assert advertised["webSocketPingPayloadBytes"] == WEBSOCKET_PING_PAYLOAD_BYTES

    selection = meta["measurementSelection"]
    assert isinstance(selection, dict)
    assert selection["capabilityVersion"] == 1
    assert selection["legacyFallback"] is False
    assert selection["downloadPath"] == DOWNLOAD_PATH
    assert selection["downloadBytesParameter"] == "n"
    assert selection["downloadPayloadParameter"] == "kind"
    assert selection["downloadFramingParameter"] == "wire"
    assert selection["downloadChunkBytesParameter"] == "block"
    assert selection["downloadFlushParameter"] == "emit"
    assert selection["downloadPayload"] == "random"
    assert selection["downloadFraming"] == "fixed"
    assert selection["downloadChunkBytes"] == DEFAULT_CHUNK
    assert selection["downloadFlush"] is False
    assert selection["uploadPath"] == UPLOAD_PATH
    assert selection["uploadBytesParameter"] == "expected"
    assert selection["uploadContentEncoding"] == "identity"
    assert selection["latencyPath"] == PING_PATH
    assert selection["latencyMethod"] == "GET"
    assert selection["latencyUsesDownloadEndpoint"] is False
    assert selection["webSocketPingPath"] == WEBSOCKET_PING_PATH
    assert selection["webSocketPingProtocol"] == WEBSOCKET_PING_PROTOCOL
    assert selection["webSocketPingPayloadBytes"] == WEBSOCKET_PING_PAYLOAD_BYTES
    assert selection["preferredLatencyTransport"] == "websocket"
    assert selection["httpFallbackAvailable"] is True
    assert selection["warmConnectionPing"] is True
    assert selection["noTransform"] is True
    assert selection["proxyBufferSuppressionHeader"] == "X-Accel-Buffering: no"

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
    assert all(sample.get("connectionReused") is True for sample in latency)
    assert all(sample.get("probeTransport") == "websocket" for sample in latency)
    assert all(sample.get("probeMethod") == "MESSAGE" for sample in latency)
    assert all(sample.get("probePath") == WEBSOCKET_PING_PATH for sample in latency)
    assert all(
        sample.get("webSocketProtocol") == WEBSOCKET_PING_PROTOCOL
        for sample in latency
    )
    assert all("probeFallbackReason" not in sample for sample in latency)
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
    server.websocket_lock = threading.Lock()  # type: ignore[attr-defined]
    server.websocket_upgrade_attempts = 0  # type: ignore[attr-defined]
    server.websocket_connections = 0  # type: ignore[attr-defined]
    server.websocket_messages = 0  # type: ignore[attr-defined]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        url = f"http://127.0.0.1:{server.server_port}"
        validate(run_client(binary, url, "download", provider="netspeed"), "download")
        validate(run_client(binary, url, "upload", provider="auto"), "upload")

        explicit = run_client(
            binary,
            url,
            "download",
            label="explicit transport",
            provider="netspeed",
            extra_args=[
                "--download-payload",
                "zero",
                "--download-framing",
                "chunked",
                "--download-chunk-bytes",
                "8192",
                "--download-flush",
                "false",
            ],
        )
        explicit_selection = explicit["meta"]["measurementSelection"]
        assert explicit_selection["downloadPayload"] == "zero"
        assert explicit_selection["downloadFraming"] == "chunked"
        assert explicit_selection["downloadChunkBytes"] == 8192
        assert explicit_selection["downloadFlush"] is False

        server.mode = "legacy"  # type: ignore[attr-defined]
        legacy = run_client(binary, url, "download", provider="netspeed")
        assert "measurementCapabilities" not in legacy["meta"]
        assert legacy["meta"]["measurementSelection"]["legacyFallback"] is True
        run_expected_failure(
            binary,
            url,
            "download",
            provider="netspeed",
            extra_args=["--download-payload", "zero"],
            expected_status=2,
        )

        server.mode = "split-cache-control"  # type: ignore[attr-defined]
        split_cache = run_client(binary, url, "download", provider="netspeed")
        assert split_cache["summary"]["downloadMbps"] > 0

        server.mode = "head-only-ping"  # type: ignore[attr-defined]
        head_only = run_client(binary, url, "download", provider="netspeed")
        assert head_only["meta"]["measurementSelection"]["latencyMethod"] == "HEAD"
        assert head_only["meta"]["measurementSelection"]["preferredLatencyTransport"] == "http"
        assert all(
            sample.get("probeMethod") == "HEAD"
            for sample in head_only["latencySamples"]
        )

        server.mode = "websocket-reject"  # type: ignore[attr-defined]
        with server.websocket_lock:  # type: ignore[attr-defined]
            attempts_before = server.websocket_upgrade_attempts  # type: ignore[attr-defined]
        websocket_fallback = run_client(
            binary, url, "download", label="WebSocket fallback", provider="netspeed"
        )
        fallback_samples = websocket_fallback["latencySamples"]
        assert fallback_samples
        assert all(sample.get("probeTransport") == "http" for sample in fallback_samples)
        assert all(sample.get("probeMethod") == "GET" for sample in fallback_samples)
        assert all(sample.get("probePath") == PING_PATH for sample in fallback_samples)
        assert all(
            "websocket" in sample.get("probeFallbackReason", "").lower()
            for sample in fallback_samples
        )
        with server.websocket_lock:  # type: ignore[attr-defined]
            attempts_after = server.websocket_upgrade_attempts  # type: ignore[attr-defined]
        assert attempts_after - attempts_before == 1

        server.mode = "websocket-bad-echo"  # type: ignore[attr-defined]
        bad_echo_fallback = run_client(
            binary, url, "download", label="WebSocket bad echo fallback", provider="netspeed"
        )
        assert all(
            sample.get("probeTransport") == "http"
            and "websocket" in sample.get("probeFallbackReason", "").lower()
            for sample in bad_echo_fallback["latencySamples"]
        )

        server.mode = "websocket-stacked-encoding"  # type: ignore[attr-defined]
        stacked_encoding_fallback = run_client(
            binary, url, "download", label="WebSocket encoding fallback", provider="netspeed"
        )
        assert all(
            sample.get("probeTransport") == "http"
            and "content-encoding" in sample.get("probeFallbackReason", "").lower()
            for sample in stacked_encoding_fallback["latencySamples"]
        )

        server.mode = "old-protocol"  # type: ignore[attr-defined]
        # An endpoint that identifies itself as Netspeed is never downgraded to
        # Cloudflare mode, even when an intermediary adds a Cloudflare header.
        run_expected_failure(binary, url, "download", provider="auto")
        server.mode = "truncated-download"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "bad-upload-receipt"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "upload")
        server.mode = "bad-upload-header"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "upload")
        server.mode = "encoded-download"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "stacked-encoding"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "payload-mismatch"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "framing-mismatch"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "chunk-mismatch"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "flush-mismatch"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "missing-no-transform"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "missing-proxy-suppression"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
        server.mode = "cold-latency"  # type: ignore[attr-defined]
        run_expected_failure(binary, url, "download")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
    print("C protocol-v2 HTTP transport process tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
