from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "netspeed_markdown_links", ROOT / "scripts" / "check_markdown_links.py"
)
assert SPEC and SPEC.loader
links = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = links
SPEC.loader.exec_module(links)


class MarkdownLinkTests(unittest.TestCase):
    def test_normalize_target(self) -> None:
        self.assertEqual(links.normalize_target("docs/example.md#part"), "docs/example.md")
        self.assertEqual(links.normalize_target("<docs/example file.md>"), "docs/example file.md")
        self.assertIsNone(links.normalize_target("https://example.com/a"))
        self.assertIsNone(links.normalize_target("#local-heading"))

    def test_local_targets_ignore_fenced_code(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "README.md"
            path.write_text(
                "[kept](docs/kept.md)\n"
                "```md\n[ignored](docs/missing.md)\n```\n"
                "[reference]: docs/reference.md#heading\n",
                encoding="utf-8",
            )
            self.assertEqual(
                links.local_targets(path),
                [(1, "docs/kept.md"), (5, "docs/reference.md")],
            )


if __name__ == "__main__":
    unittest.main()
