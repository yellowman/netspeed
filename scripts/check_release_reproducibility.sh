#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
version=${RELEASE_TEST_VERSION:-v0.0.0-ci}
tmp=${TMPDIR:-/tmp}/netspeed-release-repro-$$
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
mkdir -p "$tmp/a" "$tmp/b"

cd "$root"
commit=$(git rev-parse HEAD)
source_date=$(git show -s --format=%cI HEAD)
${MAKE:-make} c-client WEBRTC=no VERSION="$version" COMMIT="$commit" SOURCE_DATE="$source_date"
python3 scripts/release.py --version "$version" --output "$tmp/a" \
  --c-binary linux/amd64=bin/netspeed-c --require-c-platform linux/amd64
python3 scripts/release.py --version "$version" --output "$tmp/b" \
  --c-binary linux/amd64=bin/netspeed-c --require-c-platform linux/amd64
python3 scripts/compare_release_dirs.py "$tmp/a" "$tmp/b"

# Deterministic metadata must identify the compiler, never the build host.
python3 - "$tmp/a/release-manifest.json" <<'PY'
import json
from pathlib import Path
import re
import sys
manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
version = manifest.get("goVersion", "")
if not re.fullmatch(r"go[0-9]+(?:\.[0-9]+)+(?:[A-Za-z0-9.-]+)?", version):
    raise SystemExit(f"host-dependent or invalid goVersion: {version!r}")
print(f"release compiler identity: {version}")
PY
