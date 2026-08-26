#!/usr/bin/env python3
"""Reject generated binaries and release detritus from the Git source tree."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

BINARY_SUFFIXES = {
    ".a", ".class", ".dll", ".dylib", ".exe", ".jar", ".lib", ".o",
    ".obj", ".pdb", ".pyc", ".so",
}
BINARY_NAMES = {"netspeed", "netspeedd"}
MAGICS = (
    b"\x7fELF",
    b"MZ",
    b"\xfe\xed\xfa\xce",
    b"\xce\xfa\xed\xfe",
    b"\xfe\xed\xfa\xcf",
    b"\xcf\xfa\xed\xfe",
)
MAX_TRACKED_BYTES = 5 * 1024 * 1024


def git_files(root: Path) -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "-z"], cwd=root, check=True, capture_output=True
    )
    return [root / os.fsdecode(item) for item in result.stdout.split(b"\0") if item]


def inspect(root: Path) -> list[str]:
    problems: list[str] = []
    for path in git_files(root):
        rel = path.relative_to(root).as_posix()
        if not path.exists():
            problems.append(f"tracked path is missing: {rel}")
            continue
        if path.is_symlink():
            continue
        if not path.is_file():
            continue
        size = path.stat().st_size
        if size > MAX_TRACKED_BYTES:
            problems.append(f"tracked file exceeds 5 MiB: {rel} ({size} bytes)")
        if path.suffix.lower() in BINARY_SUFFIXES:
            problems.append(f"tracked build artifact suffix: {rel}")
        if path.name in BINARY_NAMES and "/" not in rel:
            problems.append(f"tracked top-level executable name: {rel}")
        with path.open("rb") as handle:
            head = handle.read(4)
        if any(head.startswith(magic) for magic in MAGICS):
            problems.append(f"tracked native executable/object: {rel}")

    go_mod = root / "go.mod"
    if go_mod.exists():
        contents = go_mod.read_text(encoding="utf-8")
        if re.search(r"(?m)^\s*replace(?:\s|\()", contents):
            problems.append("go.mod contains a replace directive")
    return problems


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    root = args.root.resolve()
    problems = inspect(root)
    if problems:
        for problem in problems:
            print(f"source hygiene: {problem}", file=sys.stderr)
        return 1
    print("source hygiene: clean")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
