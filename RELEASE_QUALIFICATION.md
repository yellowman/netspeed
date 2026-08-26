# Netspeed release qualification

This document defines the release gate introduced in Phase 6. A source tree is
not release-qualified merely because it compiles on one workstation. A release
is qualified only when the real dependency graph and the required process,
browser, operating-system, security, and reproducibility checks pass for the
exact tagged commit.

## 1. Supported release surface

Official release artifacts contain:

- the Go `netspeed` protocol-v2 client;
- the Go `netspeedd` daemon;
- the browser UI and static assets;
- deployment examples and canonical protocol documentation.

The legacy C implementation under `netspeed.c/` is retained in the source tree
but is not a supported client. It predates measurement protocol v2 and is not
included in binary release archives. CI compiles it with GCC and Clang using
warnings as errors as a source-health check only.

## 2. Required CI gates

The workflow in `.github/workflows/ci.yml` performs the following against the
actual modules in `go.mod`; compile-time stubs and local `replace` directives are
not accepted. Go 1.27.0 is pinned for release construction and security analysis,
while Go 1.21.3 remains the compatibility floor:

1. `go mod download`, `go mod verify`, and a clean `go mod tidy` result;
2. Go tests at the module minimum, Go 1.21.3, and the pinned Go 1.27.0 release toolchain;
3. the race detector, `go vet`, Staticcheck, and `govulncheck`;
4. browser-engine unit tests and a real Chromium transfer smoke test;
5. daemon/CLI process tests, HTTP boundary tests, and embedded Pion/TURN packet
   reconciliation over exact 1,200-byte frames;
6. native Windows build and tests;
7. native OpenBSD build and tests inside an OpenBSD virtual machine;
8. source-hygiene and local Markdown-link validation;
9. GCC and Clang source-health builds for the unsupported C client;
10. commit-pinned workflow actions with write permission limited to publishing;
11. two independent cross-platform release builds whose artifacts must match
   byte for byte.

A failed or skipped required gate blocks a release.

## 3. End-to-end fixtures

`tests/integration/e2e_test.go` builds both commands from the checked-out source,
launches a real daemon, and verifies:

- `/meta` protocol and transfer capabilities;
- exact download length and cache policy;
- exact upload receipt accounting;
- known-length oversize rejection;
- quick download-only and upload-only CLI results;
- optional real WebRTC and embedded-TURN packet-test completion.

The TURN test is enabled with:

```sh
NETSPEED_E2E_TURN=1 make integration-turn
```

`tests/browser/smoke.spec.js` launches the daemon through Playwright, loads the
actual UI in Chromium, fetches protocol metadata, streams a download, and checks
an upload receipt from browser JavaScript.

## 4. Reproducible release construction

`scripts/release.py` creates deterministic ZIP archives for:

- Linux amd64 and arm64;
- OpenBSD amd64 and arm64;
- Windows amd64 and arm64;
- the complete tracked source tree.

The builder:

- requires a clean Git tree for an official release;
- derives version, commit, and date from the exact tag and commit;
- honors `SOURCE_DATE_EPOCH`, defaulting to the commit timestamp;
- uses `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, and an empty Go build ID;
- injects one shared version/commit/date record into both Go commands;
- writes archive members in stable order with normalized timestamps and modes;
- emits `SHA256SUMS` and `release-manifest.json`;
- refuses unsafe output paths, symlink outputs, and directories containing unknown files;
- excludes the unsupported C executable from binary archives.

The CI reproducibility job runs the complete builder twice and compares every
output file by SHA-256. The release workflow repeats the same comparison before
publishing a tag.

## 5. Source hygiene

`scripts/check_source_hygiene.py` rejects tracked native binaries, object files,
libraries, Java archives, Python bytecode, top-level `netspeed`/`netspeedd`
executables, files larger than 5 MiB, and `go.mod` replacement directives.

This makes stale executable detection a release invariant rather than a manual
review item. `scripts/check_markdown_links.py` separately rejects broken local
documentation links before qualification or publishing.

## 6. Local commands

With the real Go modules available:

```sh
make fmt-check
make hygiene
make docs-check
make release-tools
make test
make race
make vet
make web-test
make c-check
make integration
NETSPEED_E2E_TURN=1 make integration-turn
```

After installing `@playwright/test` and Chromium:

```sh
make browser-smoke
```

Build an exact tagged release:

```sh
python3 scripts/release.py --output dist
```

For an untagged CI or developer rehearsal, supply an explicit version. Official
publishing still requires a clean, tagged commit:

```sh
python3 scripts/release.py --version v0.0.0-ci --output dist
```

## 7. Publishing

`.github/workflows/release.yml` runs on version tags. It reruns the real module,
test, race, process, TURN, source-health, and reproducibility gates before using
GitHub's release API to publish the generated archives, manifest, and checksums.
