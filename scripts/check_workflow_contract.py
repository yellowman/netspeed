#!/usr/bin/env python3
"""Validate that documented release gates are executable GitHub workflows."""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import shlex
import sys

WORKFLOWS = (
    Path(".github/workflows/ci.yml"),
    Path(".github/workflows/release.yml"),
)
RELEASE_NEEDS = {
    "source-contract",
    "go-minimum",
    "go-release",
    "race",
    "c-client",
    "integration",
    "chromium",
    "windows",
    "openbsd",
    "release-reproducibility",
}
LOCAL_CI_TARGETS = {
    "fmt-check",
    "hygiene",
    "docs-check",
    "make-portability-check",
    "workflow-contract-check",
    "release-tools",
    "mod-tidy-check",
    "test-go",
    "test-race",
    "vet",
    "staticcheck",
    "vuln",
    "web-test",
    "c-check",
    "c-sanitize",
    "test-parity",
    "integration",
    "integration-turn",
    "browser-smoke",
    "c-protocol-check",
    "c-client",
    "integration-c-turn",
    "release-reproducibility",
}


def make_targets(path: Path) -> set[str]:
    targets: set[str] = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line or line[0].isspace() or line.startswith("#") or "=" in line.split(":", 1)[0]:
            continue
        match = re.match(r"^([A-Za-z0-9_.-]+(?:\s+[A-Za-z0-9_.-]+)*)\s*:", line)
        if match:
            targets.update(match.group(1).split())
    return targets


def make_invocations(text: str) -> list[tuple[str, str]]:
    calls: list[tuple[str, str]] = []
    for raw in text.splitlines():
        if "make" not in raw:
            continue
        try:
            words = shlex.split(raw.strip().lstrip("-"))
        except ValueError:
            continue
        for index, word in enumerate(words):
            if word not in {"make", "gmake", "pmake", "${MAKE}", "$(MAKE)"}:
                continue
            directory = "."
            cursor = index + 1
            while cursor < len(words):
                token = words[cursor]
                if token == "-C" and cursor + 1 < len(words):
                    directory = words[cursor + 1]
                    cursor += 2
                    continue
                if token.startswith("-"):
                    cursor += 1
                    continue
                if "=" in token and not token.startswith("="):
                    cursor += 1
                    continue
                if re.fullmatch(r"[A-Za-z0-9_.-]+", token):
                    calls.append((directory, token))
                break
    return calls


def target_block(text: str, name: str) -> str:
    lines = text.splitlines()
    start = None
    for index, line in enumerate(lines):
        if re.match(rf"^{re.escape(name)}\s*:", line):
            start = index
            break
    if start is None:
        return ""
    end = start + 1
    while end < len(lines):
        line = lines[end]
        if line and not line[0].isspace() and not line.startswith("#"):
            break
        end += 1
    return "\n".join(lines[start:end])


def inspect(root: Path) -> list[str]:
    problems: list[str] = []
    root_make = root / "Makefile"
    c_make = root / "netspeed.c" / "Makefile"
    root_targets = make_targets(root_make) if root_make.is_file() else set()
    c_targets = make_targets(c_make) if c_make.is_file() else set()

    for relative in WORKFLOWS:
        path = root / relative
        if not path.is_file():
            problems.append(f"missing workflow: {relative}")
            continue
        text = path.read_text(encoding="utf-8")
        for action, ref in re.findall(r"(?m)^\s*-?\s*uses:\s*([^@\s]+)@([^\s#]+)", text):
            if action.startswith("./"):
                continue
            if not re.fullmatch(r"[0-9a-f]{40}", ref):
                problems.append(f"{relative}: action is not commit-pinned: {action}@{ref}")
        for directory, target in make_invocations(text):
            available = c_targets if directory.rstrip("/") == "netspeed.c" else root_targets
            if target not in available:
                problems.append(f"{relative}: make target does not exist: {directory}:{target}")

    release_path = root / ".github/workflows/release.yml"
    if release_path.is_file():
        release_text = release_path.read_text(encoding="utf-8")
        publish = target_block(release_text, "build-and-publish")
        # YAML jobs are indented, so find the job and inspect through the next peer job.
        match = re.search(
            r"(?ms)^  build-and-publish:\n(?P<body>.*?)(?=^  [A-Za-z0-9_.-]+:\n|\Z)",
            release_text,
        )
        body = match.group("body") if match else publish
        needs_match = re.search(r"(?ms)^    needs:\s*\[(.*?)\]", body)
        needs = set()
        if needs_match:
            needs = {value.strip() for value in needs_match.group(1).split(",") if value.strip()}
        missing = RELEASE_NEEDS - needs
        if missing:
            problems.append("release build-and-publish is missing needs: " + ", ".join(sorted(missing)))
        if release_text.count("contents: write") != 1 or "contents: write" not in body:
            problems.append("release write permission must appear exactly once in build-and-publish")

    root_text = root_make.read_text(encoding="utf-8") if root_make.is_file() else ""
    ci_block = target_block(root_text, "ci")
    missing_local = {target for target in LOCAL_CI_TARGETS if target not in ci_block}
    if missing_local:
        problems.append("local ci target omits required gates: " + ", ".join(sorted(missing_local)))
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    problems = inspect(args.root.resolve())
    if problems:
        print("\n".join(f"workflow contract: {problem}" for problem in problems), file=sys.stderr)
        return 1
    print("workflow contract: complete and commit-pinned")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
