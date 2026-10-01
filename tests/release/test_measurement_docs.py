from __future__ import annotations

import json
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]
CONTRACTS = ("MEASUREMENT_PROTOCOL_V2.md", "HTTP_MEASUREMENT_TRANSPORT.md")


class MeasurementDocumentationTests(unittest.TestCase):
    def test_censored_rtt_examples_match_browser_contract(self) -> None:
        for name in CONTRACTS:
            with self.subTest(document=name):
                document = (ROOT / name).read_text(encoding="utf-8")
                examples = [
                    json.loads(block)
                    for block in re.findall(r"```json\n(.*?)\n```", document, re.DOTALL)
                    if '"rawRttMs"' in block
                ]
                self.assertEqual(len(examples), 1, "include one censored RTT example")
                sample = examples[0]
                self.assertEqual(sample["rawRttMs"], 0)
                self.assertIsNone(sample["rttMs"])
                self.assertIs(sample["timingResolutionLimited"], True)

    def test_contracts_do_not_specify_positive_rtt_floor(self) -> None:
        for name in CONTRACTS:
            with self.subTest(document=name):
                document = (ROOT / name).read_text(encoding="utf-8")
                self.assertNotIn("representation floor", document)
                self.assertNotIn("timerRepresentationFloorMs", document)

    def test_idle_and_loaded_summary_statistics_are_distinct(self) -> None:
        document = (ROOT / "MEASUREMENT_PROTOCOL_V2.md").read_text(encoding="utf-8")
        self.assertIn("unloaded latency: R-7 median", document)
        self.assertIn("download-loaded and upload-loaded latency: R-7 p90", document)


if __name__ == "__main__":
    unittest.main()
