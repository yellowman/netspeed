#!/usr/bin/env python3
"""Compare the Go and C clients against one protocol-v2 process fixture."""

from __future__ import annotations

from http.server import ThreadingHTTPServer
import importlib.util
from pathlib import Path
import sys
import threading
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
FIXTURE_PATH = ROOT / "netspeed.c" / "tests" / "test_client.py"


def load_fixture() -> Any:
    spec = importlib.util.spec_from_file_location("netspeed_client_fixture", FIXTURE_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load fixture {FIXTURE_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def result_shape(value: Any) -> tuple[Any, ...]:
    if isinstance(value, dict):
        return (
            "object",
            tuple(sorted((key, result_shape(item)) for key, item in value.items())),
        )
    if isinstance(value, list):
        return ("array", tuple(sorted({result_shape(item) for item in value})))
    if value is None:
        return ("null",)
    if isinstance(value, bool):
        return ("boolean",)
    if isinstance(value, (int, float)):
        return ("number",)
    if isinstance(value, str):
        return ("string",)
    return (type(value).__name__,)


def compare_results(go_result: dict[str, object], c_result: dict[str, object], direction: str) -> None:
    if result_shape(go_result) != result_shape(c_result):
        raise AssertionError(f"Go/C {direction} JSON result schemas differ")

    for key in ("meta", "quality"):
        if go_result[key] != c_result[key]:
            raise AssertionError(f"Go/C {direction} {key} values differ")

    go_confidence = go_result["testConfidence"]
    c_confidence = c_result["testConfidence"]
    assert isinstance(go_confidence, dict) and isinstance(c_confidence, dict)
    for key in ("overall", "overallScore", "warnings"):
        if go_confidence[key] != c_confidence[key]:
            raise AssertionError(f"Go/C {direction} confidence {key} differs")

    go_metrics = go_confidence["metrics"]
    c_metrics = c_confidence["metrics"]
    assert isinstance(go_metrics, dict) and isinstance(c_metrics, dict)
    for key in ("sampleCount", "loadedOverlap", "timingAccuracy", "packetTest"):
        if go_metrics[key] != c_metrics[key]:
            raise AssertionError(f"Go/C {direction} confidence metric {key} differs")


def run_direction(fixture: Any, go_binary: Path, c_binary: Path, direction: str) -> None:
    server = ThreadingHTTPServer(("127.0.0.1", 0), fixture.ProtocolHandler)
    server.mode = "normal"  # type: ignore[attr-defined]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        server_url = f"http://127.0.0.1:{server.server_port}"
        go_result = fixture.run_client(go_binary, server_url, direction, "Go client")
        c_result = fixture.run_client(c_binary, server_url, direction, "C client")
        fixture.validate(go_result, direction)
        fixture.validate(c_result, direction)
        compare_results(go_result, c_result, direction)
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def main() -> int:
    if len(sys.argv) != 3:
        raise SystemExit("usage: client_parity.py PATH-TO-GO-CLIENT PATH-TO-C-CLIENT")
    go_binary = Path(sys.argv[1]).resolve()
    c_binary = Path(sys.argv[2]).resolve()
    for binary in (go_binary, c_binary):
        if not binary.is_file():
            raise SystemExit(f"client binary does not exist: {binary}")

    fixture = load_fixture()
    run_direction(fixture, go_binary, c_binary, "download")
    run_direction(fixture, go_binary, c_binary, "upload")
    print("Go/C protocol-v2 process parity passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
