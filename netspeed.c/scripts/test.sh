#!/bin/sh
set -eu
bin=${1:-netspeed}
"./${bin}" --help >/dev/null
"./${bin}" --provider netspeed --help >/dev/null
python3 tests/cloudflare_fixture.py "./${bin}"
