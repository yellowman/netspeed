#!/usr/bin/env python3
"""Compare two release directories byte-for-byte."""

from __future__ import annotations

import argparse
import hashlib
from pathlib import Path
import sys


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inventory(root: Path) -> dict[str, str]:
    return {
        path.relative_to(root).as_posix(): digest(path)
        for path in sorted(root.rglob("*"))
        if path.is_file()
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("left", type=Path)
    parser.add_argument("right", type=Path)
    args = parser.parse_args()
    left = inventory(args.left)
    right = inventory(args.right)
    if left != right:
        for name in sorted(set(left) | set(right)):
            if left.get(name) != right.get(name):
                print(f"release mismatch: {name}: {left.get(name)} != {right.get(name)}", file=sys.stderr)
        return 1
    print(f"release reproducibility: {len(left)} files match")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
