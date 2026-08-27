# Netspeed release qualification

This document defines the blocking release gate. A source tree is not
release-qualified merely because it compiles on one workstation. A release is
qualified only when the real dependency graph and the required process,
browser, operating-system, native-client, security, and reproducibility checks
pass for the exact tagged commit.

## 1. Supported release surface

Supported components are:

- the Go `netspeed` protocol-v2 client;
- the native C `netspeed-c` protocol-v2 client;
- the Go `netspeedd` daemon;
- the browser protocol-v2 client and static UI;
- deployment examples and canonical protocol documentation.

The C implementation is an independent supported client. It must satisfy the
same verified-transfer, sustained-window, loaded-overlap, statistics, result,
and exact packet-test contract as the Go client. Its detailed boundary is
[`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md).

A platform archive contains `netspeed-c` only when the release workflow supplies
a qualified native executable for that exact OS/architecture. `BUILDINFO.txt`
and `release-manifest.json` state which C platforms were included. The current
publishing workflow requires a complete `WEBRTC=yes` Linux/amd64 C executable;
it does not silently publish an HTTP-only C build.

## 2. Required CI gates

The executable policies live in `.github/workflows/ci.yml` and
`.github/workflows/release.yml`. Both use the actual modules in `go.mod`;
compile-time stubs and local `replace` directives are not accepted. Go 1.27.0
is pinned for release construction and security analysis, while Go 1.21.3
remains the module compatibility floor.

`scripts/check_workflow_contract.py` is itself a blocking gate. It fails when a
required workflow is missing, an external action is not pinned to a full commit
SHA, a workflow invokes a nonexistent Make target, the local `ci` target omits a
portable release gate, release write permission appears outside the publishing
job, or the publishing job does not depend on every required qualification job.

Required gates are:

1. `go mod download`, `go mod verify`, and a clean `go mod tidy` result;
2. Go tests at Go 1.21.3 and the pinned Go 1.27.0 release toolchain;
3. the race detector, `go vet`, Staticcheck, and `govulncheck`;
4. browser-engine unit tests and a real Chromium transfer smoke test;
5. daemon/Go-CLI process tests, HTTP boundary tests, and embedded Pion/TURN
   reconciliation over exact 1,200-byte frames;
6. native Windows Go builds and tests;
7. native OpenBSD Go builds and tests inside an OpenBSD virtual machine;
8. root and C Makefile parsing/execution by OpenBSD base `make` plus a source
   check that rejects GNU-only make constructs;
9. strict GCC and Clang C-client builds with warnings as errors;
10. C protocol/statistics/frame units, positive and negative protocol-v2 process
    fixtures, and AddressSanitizer/UndefinedBehaviorSanitizer runs;
11. a pinned real libdatachannel build and C-client-to-Pion-daemon packet test
    through embedded TURN;
12. source-hygiene and local Markdown-link validation;
13. commit-pinned workflow actions with release write permission limited to the
    final publishing job;
14. two independent cross-platform release constructions whose artifacts must
    match byte for byte.

The release workflow exposes those boundaries as separate jobs named
`source-contract`, `go-minimum`, `go-release`, `race`, `c-client`,
`integration`, `chromium`, `windows`, `openbsd`, and
`release-reproducibility`. `build-and-publish` declares all ten in `needs`; a
failed, canceled, or skipped required job therefore prevents publication.

A failed or skipped required gate blocks a release.

## 3. End-to-end fixtures

`tests/integration/e2e_test.go` builds the Go commands, launches a real daemon,
and verifies:

- `/meta` protocol and transfer capabilities;
- exact download length and cache policy;
- exact upload receipt accounting;
- known-length oversize rejection;
- quick download-only and upload-only Go CLI results;
- real Go/Pion and embedded-TURN packet completion;
- real C/libdatachannel to Pion-daemon packet completion, exact frame size, and
  directional counter consistency.

The TURN fixtures are enabled with:

```sh
NETSPEED_E2E_TURN=1 make integration-turn

NETSPEED_C_CLIENT="$PWD/bin/netspeed-c" \
NETSPEED_E2E_TURN=1 \
make integration-c-turn
```

`netspeed.c/tests/test_client.py` separately runs the native client against a
protocol-v2 HTTP fixture. It checks explicit `netspeed`, safe `auto`,
authenticated positive download/upload runs, refusal to downgrade an identified
but incompatible Netspeed endpoint, truncated downloads, and incorrect upload
receipts.

`netspeed.c/tests/test_packet_report_rejection.py` serves an HTTP-200 packet
report whose counters are internally consistent but whose authoritative
`ok` field is `false`. The C client must mark that measurement unavailable with
`server rejected packet report`; it may never publish the counters as valid
loss data.

`tests/browser/smoke.spec.js` launches the daemon through Playwright, loads the
actual UI in Chromium, fetches protocol metadata, streams a download, and checks
an upload receipt from browser JavaScript. It also runs a complete test with
packet delivery unavailable, injects a failed transfer, and switches a decoded
shared result among all three presentations. Those cases require the progressive
rail to preserve `unavailable` and `failed` outcomes and require interface links
to retain only the supported `r` query parameter.

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
- builds the Go commands with `CGO_ENABLED=0`, `-trimpath`,
  `-buildvcs=false`, and an empty Go build ID;
- injects version, commit, and source date into both Go commands;
- accepts a prequalified native client through
  `--c-binary GOOS/GOARCH=PATH`;
- can require selected native artifacts through
  `--require-c-platform GOOS/GOARCH`;
- records a host-independent compiler identity from `go env GOVERSION`, not the
  host-qualified output of `go version`;
- records included C platforms in `release-manifest.json` and each archive's
  `BUILDINFO.txt`;
- writes archive members in stable order with normalized timestamps and modes;
- emits `SHA256SUMS` and `release-manifest.json`;
- refuses unsafe output paths, symlink outputs, and directories containing
  unknown files.

The CI reproducibility job includes a native C executable in the Linux/amd64
packaging test, runs the complete builder twice, and compares every output file.
The manifest and `BUILDINFO.txt` contain values such as `go1.27.0`; they never
contain a builder suffix such as `linux/amd64` or `openbsd/amd64`. The publishing
workflow repeats the comparison with the qualified `WEBRTC=yes` C release
executable before uploading release artifacts for a tag.

## 5. Native dependency pin

`scripts/install_libdatachannel.sh` builds the exact libdatachannel commit pinned
by the repository. CI builds it without media, examples, WebSockets, or upstream
tests, installs its C API and pkg-config metadata into an isolated prefix, and
links the Netspeed client using static libdatachannel metadata.

The native client still uses system ABI dependencies such as libc, libcurl, the
C++ runtime, and TLS libraries. Release notes must describe the target runtime
and cannot claim a fully static binary unless the artifact is independently
verified as such.

## 6. Source hygiene

`scripts/check_source_hygiene.py` rejects tracked native binaries, object files,
libraries, Java archives, Python bytecode, top-level `netspeed`/`netspeedd`
executables, files larger than 5 MiB, and `go.mod` replacement directives.

`scripts/check_make_portability.py` rejects GNU-only conditionals, make
functions, directives, pattern rules, and order-only prerequisites from the two
portable Makefiles. Actual OpenBSD base-make execution remains the authoritative
pmake check.

`scripts/check_markdown_links.py` rejects broken local documentation links.

## 7. Local commands

With the real Go modules available:

```sh
make fmt-check
make hygiene
make docs-check
make make-portability-check
make workflow-contract-check
make release-tools
make test
make race
make vet
make web-test
make c-check
make test-parity WEBRTC=no
make c-sanitize
make integration
NETSPEED_E2E_TURN=1 make integration-turn
```

With libdatachannel installed:

```sh
make c-client WEBRTC=yes WEBRTC_STATIC=yes
NETSPEED_C_CLIENT="$PWD/bin/netspeed-c" \
NETSPEED_E2E_TURN=1 \
make integration-c-turn
```

After installing `@playwright/test` and Chromium:

```sh
make browser-smoke
```


The complete non-platform-specific release boundary is also available as:

```sh
make ci
```

Unlike `make test`, `make ci` is intentionally not a lightweight developer
shortcut. It requires the pinned analyzers, Chromium, libdatachannel, the real
embedded-TURN fixtures, sanitizers, and reproducible release construction; a
missing tool or unavailable dependency is a failure.

Build an exact tagged release with a qualified Linux C binary:

```sh
python3 scripts/release.py \
  --output dist \
  --c-binary linux/amd64=bin/netspeed-c \
  --require-c-platform linux/amd64
```

Developer-only dirty-tree builds require `--allow-dirty`; official publishing
does not use that option.
