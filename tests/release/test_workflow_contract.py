from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "netspeed_workflow_contract", ROOT / "scripts" / "check_workflow_contract.py"
)
assert SPEC and SPEC.loader
workflow = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = workflow
SPEC.loader.exec_module(workflow)


class WorkflowContractTests(unittest.TestCase):
    def test_repository_workflows_match_the_release_contract(self) -> None:
        self.assertEqual(workflow.inspect(ROOT), [])

    def test_unpinned_action_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".github/workflows").mkdir(parents=True)
            (root / "netspeed.c").mkdir()
            (root / "Makefile").write_text("ci:\n\ttrue\n", encoding="utf-8")
            (root / "netspeed.c/Makefile").write_text("test:\n\ttrue\n", encoding="utf-8")
            for name in ("ci.yml", "release.yml"):
                (root / ".github/workflows" / name).write_text(
                    "jobs:\n  example:\n    steps:\n      - uses: actions/checkout@v7\n",
                    encoding="utf-8",
                )
            self.assertTrue(
                any("not commit-pinned" in problem for problem in workflow.inspect(root))
            )


if __name__ == "__main__":
    unittest.main()
