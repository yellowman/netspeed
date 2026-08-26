#!/usr/bin/env python3
"""Reject GNU-only constructs from Netspeed's GNU make / BSD pmake files."""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import sys

DEFAULT_FILES = ("Makefile", "netspeed.c/Makefile")
FORBIDDEN = (
    (re.compile(r"^\s*(?:ifeq|ifneq|ifdef|ifndef|else\s+ifeq|else\s+ifneq|endif)\b"), "GNU conditional"),
    (re.compile(r"\$\((?:shell|wildcard|patsubst|subst|filter|filter-out|foreach|call|eval|value|origin|flavor)\b"), "GNU make function"),
    (re.compile(r"^\s*(?:define|endef|override|export\s+override)\b"), "GNU directive"),
    (re.compile(r"^\.ONESHELL\s*:"), "GNU .ONESHELL"),
    (re.compile(r"^[^#\t\n]*:\s*[^#\n]*\|"), "GNU order-only prerequisite"),
    (re.compile(r"^[^#\t\n]*%[^#\n]*:"), "GNU pattern rule"),
)


def inspect(path: Path) -> list[str]:
    problems: list[str] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        for pattern, description in FORBIDDEN:
            if pattern.search(line):
                problems.append(f"{path}:{line_number}: {description}: {line.strip()}")
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("files", nargs="*")
    args = parser.parse_args()
    root = args.root.resolve()
    paths = [root / value for value in (args.files or DEFAULT_FILES)]
    problems: list[str] = []
    for path in paths:
        if not path.is_file():
            problems.append(f"missing makefile: {path}")
        else:
            problems.extend(inspect(path))
    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    print("portable makefile subset check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
