#!/usr/bin/env python3
"""Verify that local Markdown link targets exist in the tracked source tree."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import unquote, urlsplit

INLINE_LINK = re.compile(r"!?\[[^\]]*\]\(([^)]+)\)")
REFERENCE_LINK = re.compile(r"^\s*\[[^\]]+\]:\s*(\S+)")
FENCE = re.compile(r"^\s*(`{3,}|~{3,})")


def tracked_markdown(root: Path) -> list[Path]:
    output = subprocess.run(
        ["git", "ls-files", "-z", "--", "*.md"],
        cwd=root,
        check=True,
        capture_output=True,
    ).stdout
    return sorted(root / os.fsdecode(item) for item in output.split(b"\0") if item)


def normalize_target(raw: str) -> str | None:
    target = raw.strip()
    if target.startswith("<") and target.endswith(">"):
        target = target[1:-1].strip()
    else:
        # Inline links may include an optional quoted title after the URL. Local
        # documentation paths in this repository do not contain literal spaces.
        target = target.split(maxsplit=1)[0]
    if not target or target.startswith("#"):
        return None
    parsed = urlsplit(target)
    if parsed.scheme or parsed.netloc:
        return None
    path = unquote(parsed.path)
    return path or None


def local_targets(markdown: Path) -> list[tuple[int, str]]:
    targets: list[tuple[int, str]] = []
    fence_marker: str | None = None
    for line_number, line in enumerate(markdown.read_text(encoding="utf-8").splitlines(), 1):
        fence = FENCE.match(line)
        if fence:
            marker = fence.group(1)[0]
            if fence_marker is None:
                fence_marker = marker
            elif fence_marker == marker:
                fence_marker = None
            continue
        if fence_marker is not None:
            continue
        for match in INLINE_LINK.finditer(line):
            target = normalize_target(match.group(1))
            if target is not None:
                targets.append((line_number, target))
        reference = REFERENCE_LINK.match(line)
        if reference:
            target = normalize_target(reference.group(1))
            if target is not None:
                targets.append((line_number, target))
    return targets


def inspect(root: Path) -> list[str]:
    problems: list[str] = []
    for markdown in tracked_markdown(root):
        for line_number, target in local_targets(markdown):
            if target.startswith("/"):
                destination = root / target.lstrip("/")
            else:
                destination = markdown.parent / target
            if not destination.exists():
                rel = markdown.relative_to(root).as_posix()
                problems.append(f"{rel}:{line_number}: missing local target {target!r}")
    return problems


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    root = args.root.resolve()
    problems = inspect(root)
    if problems:
        for problem in problems:
            print(f"markdown links: {problem}", file=sys.stderr)
        return 1
    print("markdown links: clean")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
