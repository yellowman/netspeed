#!/usr/bin/env python3
"""Process-level checks for the native C Cloudflare provider adapter."""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import subprocess
import threading
import time
import urllib.parse

CHUNK = 64 * 1024
FORBIDDEN_DISCRIMINATORS = {
    "payload",
    "framing",
    "chunkBytes",
    "flush",
    "kind",
    "wire",
    "block",
    "emit",
}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "cloudflare-fixture"

    def log_message(self, *_args: object) -> None:
        return

    def _record_query(self, query: dict[str, list[str]]) -> bool:
        forbidden = sorted(FORBIDDEN_DISCRIMINATORS.intersection(query))
        if not forbidden:
            return True
        with self.server.state_lock:  # type: ignore[attr-defined]
            self.server.forbidden_queries.extend(forbidden)  # type: ignore[attr-defined]
        body = b"unrecognized discriminator query"
        self.send_response(400)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True
        return False

    def _request_controls_valid(self, *, upload: bool = False) -> bool:
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
        with self.server.state_lock:  # type: ignore[attr-defined]
            self.server.bad_request_controls += 1  # type: ignore[attr-defined]
        body = b"measurement request controls missing"
        self.send_response(400)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True
        return False

    def _response_controls(self, *, after_probe: bool = False) -> None:
        mode = self.server.mode  # type: ignore[attr-defined]
        cache_control = "no-store, no-transform"
        if mode == "anti-transform-drift" and after_probe:
            cache_control = "no-store"
        if mode == "split-cache-control":
            self.send_header("Cache-Control", "no-store")
            self.send_header("Cache-Control", "no-transform")
        else:
            self.send_header("Cache-Control", cache_control)
        if not (mode == "proxy-buffer-drift" and after_probe):
            self.send_header("X-Accel-Buffering", "no")
        self.send_header("CF-Ray", "fixture")
        self.send_header("Server-Timing", "cfReqDur;dur=0.1")

    def do_GET(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlsplit(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        if not self._record_query(query):
            return
        if parsed.path == "/meta":
            body = b"not a netspeed meta document"
            self.send_response(404)
            self.send_header("Server", "cloudflare")
            self.send_header("CF-Ray", "fixture")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if parsed.path != "/__down":
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if not self._request_controls_valid():
            return
        try:
            size = int(query.get("bytes", ["0"])[0])
        except ValueError:
            self.send_error(400)
            return
        if size < 0 or size > 64 * 1024 * 1024:
            self.send_error(413)
            return

        with self.server.state_lock:  # type: ignore[attr-defined]
            before_probe = not self.server.transport_probe_seen  # type: ignore[attr-defined]
            if size == CHUNK and before_probe:
                self.server.transport_probe_seen = True  # type: ignore[attr-defined]
                is_transport_probe = True
            else:
                is_transport_probe = False
            after_probe = self.server.transport_probe_seen and not is_transport_probe  # type: ignore[attr-defined]
            self.server.download_queries.append(set(query))  # type: ignore[attr-defined]

        # Give request-to-first-byte a stable, non-zero interval.
        time.sleep(0.001)
        self.send_response(200)
        self.send_header("Server", "cloudflare")
        self._response_controls(after_probe=after_probe)
        reported_chunk: int | str = CHUNK + 1 if self.server.mode == "chunk-drift" and after_probe else CHUNK  # type: ignore[attr-defined]
        if self.server.mode == "invalid-chunk-probe" and is_transport_probe:  # type: ignore[attr-defined]
            reported_chunk = "invalid"
        reported_framing = "chunked" if self.server.mode == "framing-drift" and after_probe else "fixed"  # type: ignore[attr-defined]
        self.send_header("X-Netspeed-Chunk-Bytes", str(reported_chunk))
        reported_flush = "invalid" if self.server.mode == "invalid-flush-probe" and is_transport_probe else "false"  # type: ignore[attr-defined]
        self.send_header("X-Netspeed-Flush", reported_flush)
        self.send_header("X-Netspeed-Framing", reported_framing)
        if self.server.mode == "encoded-probe" and is_transport_probe:  # type: ignore[attr-defined]
            self.send_header("Content-Encoding", "gzip")
        elif self.server.mode == "stacked-encoding-probe" and is_transport_probe:  # type: ignore[attr-defined]
            self.send_header("Content-Encoding", "identity")
            self.send_header("Content-Encoding", "gzip")
        self.send_header("Content-Length", str(size))
        if self.server.mode == "cold-latency" and size == 0:  # type: ignore[attr-defined]
            self.send_header("Connection", "close")
            self.close_connection = True
        self.end_headers()

        block = b"0" * CHUNK
        binary_zero_block = b"\x00" * CHUNK
        try:
            remaining = size
            sent = 0
            while remaining:
                amount = min(remaining, len(block))
                response_block = block
                if (
                    self.server.mode == "payload-tail-drift"  # type: ignore[attr-defined]
                    and after_probe
                    and sent >= CHUNK
                ):
                    response_block = binary_zero_block
                self.wfile.write(response_block[:amount])
                remaining -= amount
                sent += amount
                if remaining:
                    self.wfile.flush()
                    time.sleep(0.0005)
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            return

    def do_POST(self) -> None:  # noqa: N802
        parsed = urllib.parse.urlsplit(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        if not self._record_query(query) or not self._request_controls_valid(upload=True):
            return
        try:
            size = int(self.headers.get("Content-Length", "0"))
            expected = int(query.get("bytes", ["-1"])[0])
        except ValueError:
            self.send_error(400)
            return
        received = 0
        all_ascii_zero = True
        while received < size:
            data = self.rfile.read(min(CHUNK, size - received))
            if not data:
                break
            if data != b"0" * len(data):
                all_ascii_zero = False
            received += len(data)
            if received < size:
                time.sleep(0.0002)
        with self.server.state_lock:  # type: ignore[attr-defined]
            self.server.upload_requests += 1  # type: ignore[attr-defined]
            if not all_ascii_zero:
                self.server.bad_upload_payloads += 1  # type: ignore[attr-defined]
        if parsed.path != "/__up" or received != size or expected != size or not all_ascii_zero:
            self.send_response(400)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        body = b"ok"
        self.send_response(200)
        self.send_header("Server", "cloudflare")
        self._response_controls(after_probe=True)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def reset_server(server: ThreadingHTTPServer, mode: str = "normal") -> None:
    with server.state_lock:  # type: ignore[attr-defined]
        server.mode = mode  # type: ignore[attr-defined]
        server.transport_probe_seen = False  # type: ignore[attr-defined]
        server.download_queries = []  # type: ignore[attr-defined]
        server.upload_requests = 0  # type: ignore[attr-defined]
        server.forbidden_queries = []  # type: ignore[attr-defined]
        server.bad_request_controls = 0  # type: ignore[attr-defined]
        server.bad_upload_payloads = 0  # type: ignore[attr-defined]


def run_process(binary: str, args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [binary, *args], text=True, capture_output=True, timeout=50
    )


def validate_transport(payload: dict[str, object]) -> None:
    assert payload["provider"] == "cloudflare", payload
    assert payload["measurementContract"] == "cloudflare-http-v2", payload
    assert payload["packetTopology"] == "turn-loopback", payload
    transport = payload["httpTransport"]
    assert isinstance(transport, dict)
    assert transport["capabilitySource"] == "behavioral-probe"
    assert transport["providerDefaultsOnly"] is True
    assert transport["privateTransportDiscriminatorsSent"] is False
    assert transport["compatibilityQueryParameters"] == [
        "attempt",
        "bytes",
        "compat",
        "during",
        "id",
        "seq",
    ]
    assert transport["downloadPath"] == "/__down"
    assert transport["uploadPath"] == "/__up"
    assert transport["latencyPath"] == "/__down"
    assert transport["bytesParameter"] == "bytes"
    assert transport["uploadPayload"] == "ascii-zero"
    selection = transport["selection"]
    assert selection["downloadPayload"] == "ascii-zero"
    assert selection["downloadPayloadEvidence"] == "body-all-0x30"
    assert selection["downloadFraming"] == "fixed"
    assert selection["downloadChunkBytes"] == CHUNK
    assert selection["downloadFlush"] is False
    anti = transport["antiTransform"]
    assert anti["transportCompressionDisabled"] is True
    assert anti["requestAcceptEncoding"] == "identity"
    assert anti["requestCacheControl"] == "no-store, no-transform"
    assert anti["requestPragma"] == "no-cache"
    assert anti["uploadContentEncoding"] == "identity"
    assert anti["responseContentEncoding"] == "identity"
    assert anti["responseNoStore"] is True
    assert anti["responseNoTransform"] is True
    assert anti["proxyBufferSuppressionObserved"] is True

    latency = payload["latency"]
    assert latency["available"] is True
    assert latency["connectionReused"] is True
    assert latency["warmSamples"] >= 3
    assert latency["warmupRequests"] >= 1
    assert latency["discardedColdAttempts"] == 0
    assert latency["probeTransport"] == "http"
    assert latency["probeMethod"] == "GET"
    assert latency["probePath"] == "/__down"
    assert "HTTP/1.1" in latency["httpProtocols"]


def invoke(binary: str, args: list[str]) -> dict[str, object]:
    completed = run_process(binary, args)
    if completed.returncode:
        raise AssertionError(
            f"command failed ({completed.returncode}): {args!r}\n"
            f"stdout={completed.stdout}\nstderr={completed.stderr}"
        )
    payload = json.loads(completed.stdout)
    validate_transport(payload)
    return payload


def expected_argument_failure(binary: str, args: list[str], text: str) -> None:
    completed = run_process(binary, args)
    assert completed.returncode == 2, completed
    assert text.lower() in completed.stderr.lower(), completed.stderr


def expected_runtime_json_failure(binary: str, args: list[str], text: str) -> dict[str, object]:
    completed = run_process(binary, args)
    assert completed.returncode == 1, completed
    payload = json.loads(completed.stdout)
    assert text.lower() in json.dumps(payload).lower(), payload
    return payload


def run(binary: str) -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.state_lock = threading.Lock()  # type: ignore[attr-defined]
    reset_server(server)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{server.server_address[1]}"
    try:
        # Explicit provider mode accepts a positional server URL, the standard
        # -q quick shorthand, and the canonical packet-skip flag.
        reset_server(server)
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
        assert download["downloadLoadedLatency"]["connectionReused"] is True

        # Auto mode is selected only because the response carries a positive
        # Cloudflare fingerprint. The compatibility packet-skip alias remains
        # accepted for existing scripts.
        reset_server(server)
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
        assert upload["uploadLoadedLatency"]["connectionReused"] is True

        # Constraints matching the behavior probe are accepted without adding
        # discriminator query keys.
        reset_server(server)
        constrained = invoke(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--quick",
                "--download-only",
                "--no-packet-loss",
                "--json",
                "--download-framing",
                "fixed",
                "--download-chunk-bytes",
                str(CHUNK),
                "--download-flush",
                "false",
            ],
        )
        selection = constrained["httpTransport"]["selection"]
        assert selection["downloadFramingRequested"] == "fixed"
        assert selection["downloadChunkBytesRequested"] == CHUNK
        assert selection["downloadFlushRequested"] == "false"

        # ASCII '0' is deliberately distinguished from binary zero-fill.
        reset_server(server)
        expected_argument_failure(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--quick",
                "--download-only",
                "--no-packet-loss",
                "--json",
                "--download-payload",
                "zero",
            ],
            "provider-default payload",
        )
        reset_server(server)
        expected_argument_failure(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--quick",
                "--download-only",
                "--no-packet-loss",
                "--json",
                "--download-chunk-bytes",
                "8192",
            ],
            "provider-default chunk size",
        )

        reset_server(server, "split-cache-control")
        split_cache = invoke(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--quick",
                "--download-only",
                "--no-packet-loss",
                "--json",
            ],
        )
        assert split_cache["download"]["available"] is True

        # A server that closes every zero-byte response must not produce a
        # handshake-contaminated RTT result.
        reset_server(server, "cold-latency")
        cold = expected_runtime_json_failure(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--quick",
                "--download-only",
                "--no-packet-loss",
                "--json",
            ],
            "not reused",
        )
        assert cold["latency"]["available"] is False
        assert cold["latency"]["discardedColdAttempts"] >= 4

        # Once anti-transform behavior was observed in the probe, later loss of
        # that evidence invalidates the measurement rather than silently
        # changing the contract.
        reset_server(server, "anti-transform-drift")
        expected_runtime_json_failure(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--quick",
                "--download-only",
                "--no-packet-loss",
                "--json",
            ],
            "no-transform",
        )

        for mode in ("encoded-probe", "stacked-encoding-probe"):
            reset_server(server, mode)
            encoded = run_process(
                binary,
                [
                    "--provider",
                    "cloudflare",
                    "--server",
                    base,
                    "--quick",
                    "--download-only",
                    "--no-packet-loss",
                    "--json",
                ],
            )
            assert encoded.returncode == 1, encoded
            assert "content-encoding" in encoded.stderr.lower(), encoded.stderr

        for mode, expected in (
            ("invalid-chunk-probe", "x-netspeed-chunk-bytes"),
            ("invalid-flush-probe", "x-netspeed-flush"),
        ):
            reset_server(server, mode)
            malformed = run_process(
                binary,
                [
                    "--provider",
                    "cloudflare",
                    "--server",
                    base,
                    "--quick",
                    "--download-only",
                    "--no-packet-loss",
                    "--json",
                ],
            )
            assert malformed.returncode == 1, malformed
            assert expected in malformed.stderr.lower(), malformed.stderr

        for mode, expected in (
            ("payload-tail-drift", "payload drifted"),
            ("framing-drift", "framing"),
            ("chunk-drift", "chunk-size"),
            ("proxy-buffer-drift", "x-accel-buffering"),
        ):
            reset_server(server, mode)
            expected_runtime_json_failure(
                binary,
                [
                    "--provider",
                    "cloudflare",
                    "--server",
                    base,
                    "--quick",
                    "--download-only",
                    "--no-packet-loss",
                    "--json",
                ],
                expected,
            )

        invalid_chunk = run_process(
            binary,
            [
                "--provider",
                "cloudflare",
                "--server",
                base,
                "--download-chunk-bytes",
                str(2**31),
            ],
        )
        assert invalid_chunk.returncode == 2, invalid_chunk

        bad = run_process(binary, ["--provider", "other", "--server", base])
        assert bad.returncode == 2, bad

        with server.state_lock:  # type: ignore[attr-defined]
            assert not server.forbidden_queries, server.forbidden_queries  # type: ignore[attr-defined]
            assert server.bad_request_controls == 0  # type: ignore[attr-defined]
            assert server.bad_upload_payloads == 0  # type: ignore[attr-defined]
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    run(parser.parse_args().binary)
    print("C Cloudflare HTTP transport process tests passed")
