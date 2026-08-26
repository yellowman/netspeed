from __future__ import annotations

import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "netspeed_source_hygiene", ROOT / "scripts" / "check_source_hygiene.py"
)
assert SPEC and SPEC.loader
hygiene = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = hygiene
SPEC.loader.exec_module(hygiene)


class SourceHygieneTests(unittest.TestCase):
    def make_repository(self) -> Path:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        subprocess.run(["git", "init", "-q"], cwd=root, check=True)
        (root / "go.mod").write_text("module example.invalid/test\n\ngo 1.21.3\n", encoding="utf-8")
        (root / "README.md").write_text("clean\n", encoding="utf-8")
        subprocess.run(["git", "add", "go.mod", "README.md"], cwd=root, check=True)
        return root

    def test_clean_repository(self) -> None:
        root = self.make_repository()
        self.assertEqual(hygiene.inspect(root), [])

    def test_rejects_native_binary(self) -> None:
        root = self.make_repository()
        (root / "artifact").write_bytes(b"\x7fELF" + b"\x00" * 32)
        subprocess.run(["git", "add", "artifact"], cwd=root, check=True)
        self.assertTrue(any("native executable" in problem for problem in hygiene.inspect(root)))

    def test_rejects_grouped_replace(self) -> None:
        root = self.make_repository()
        (root / "go.mod").write_text(
            "module example.invalid/test\n\ngo 1.21.3\n\nreplace (\n\texample.invalid/a => ../a\n)\n",
            encoding="utf-8",
        )
        subprocess.run(["git", "add", "go.mod"], cwd=root, check=True)
        self.assertIn("go.mod contains a replace directive", hygiene.inspect(root))


if __name__ == "__main__":
    unittest.main()
