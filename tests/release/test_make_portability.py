from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "netspeed_make_portability", ROOT / "scripts" / "check_make_portability.py"
)
assert SPEC and SPEC.loader
portability = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = portability
SPEC.loader.exec_module(portability)


class MakePortabilityTests(unittest.TestCase):
    def inspect_text(self, value: str) -> list[str]:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "Makefile"
            path.write_text(value, encoding="utf-8")
            return portability.inspect(path)

    def test_common_subset_is_accepted(self) -> None:
        self.assertEqual(
            self.inspect_text("GO?=go\nall:\n\t${GO} build ./cmd/netspeed\n"),
            [],
        )

    def test_gnu_function_is_rejected(self) -> None:
        self.assertTrue(self.inspect_text("FILES=$(wildcard *.go)\n"))

    def test_order_only_prerequisite_is_rejected(self) -> None:
        self.assertTrue(self.inspect_text("target: input | directory\n"))


if __name__ == "__main__":
    unittest.main()
