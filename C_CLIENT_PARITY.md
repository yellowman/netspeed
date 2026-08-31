# Native C client parity

The program in `netspeed.c/` is a first-class Netspeed measurement-protocol-v2
client. It independently implements the same measurement contract as the Go
`cmd/netspeed` client.

The release binary is named `netspeed-c` so it can be installed beside the Go
`netspeed` executable. Both clients talk to the same `netspeedd` daemon and emit
the same result model.

## 1. Protocol and methodology parity

The C client implements the following v2 invariants:

- requires `measurementProtocolVersion: 2` from `/meta`;
- negotiates `maxTransferBytes`, `maxConcurrentTransfersPerClient`,
  `uploadReceiptVersion`, and `packetLossFrameVersion`;
- sends the optional bearer token on metadata, transfer, latency, TURN, and
  packet-test requests;
- validates and negotiates `measurementCapabilities` version 1, including
  advertised endpoint paths, distinct query-parameter names, payload and
  framing choices, chunk bounds, upload identity coding, the exact optional
  `netspeed.ping.v1` WebSocket contract, warm HTTP fallback, and anti-transform
  controls;
- supports `random` and `zero` download payloads, `fixed` and `chunked`
  framing, explicit application chunk size, and per-chunk flush selection;
- rejects explicit transport controls on legacy or incompatible servers rather
  than silently dropping them;
- sends `Accept-Encoding: identity`, `Cache-Control: no-store, no-transform`,
  and `Pragma: no-cache` on every HTTP measurement request and disables
  libcurl's automatic content decoding;
- rejects declared payload, framing, chunk, compression, cache-control,
  proxy-buffer-suppression, upload-byte, or ingestion-duration mismatches and
  exact download-size mismatches; strict random/zero body-content sampling
  remains a separate hardening item;
- establishes one persistent WebSocket latency echo through libcurl's connected
  socket when the exact path, subprotocol, and 16-byte payload are advertised,
  excludes upgrade and one warmup from RTT, and permanently falls back to the
  advertised warm HTTP endpoint after any upgrade or message failure;
- discards cold HTTP probes when warm reuse is promised and records the actual
  probe path, method, transport, fallback reason, subprotocol, and
  connection-reuse evidence;
- rejects non-success HTTP responses, unexpected content types, redirects,
  truncated downloads, declared-length mismatches, and non-positive timing;
- streams upload bodies from a bounded generator rather than allocating the
  requested transfer size;
- accepts an upload only when the v1 receipt reports the exact transmitted byte
  count and a positive server body-read duration;
- performs three verified 100 KB and three verified 1 MB baseline requests per
  tested direction;
- selects bounded chunks and concurrent flows from the same baseline-rate
  thresholds as the Go client;
- measures one 1-second fixed window in quick mode or three 1.5-second windows
  in normal mode;
- admits only fully completed, byte-verified requests into a throughput window;
- reserves one advertised per-client transfer slot for loaded-latency probes;
- accepts a loaded-latency probe only when an active transfer request spans the
  entire request-written-to-first-byte interval without a zero-load generation gap;
- uses R-7 percentiles, conservative 1.5-IQR filtering, two-sample unloaded
  warmup removal, p90 fixed-window throughput, p90-minus-median jitter, and
  population coefficient of variation;
- calculates the same quality grades and five confidence gates used by the Go
  and browser implementations;
- preserves missing packet data as JSON `null` and human-readable `N/A` rather
  than converting an unavailable measurement into zero loss.

The canonical wire and statistical definitions remain
[`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md). Changes to that
contract must update and test all three implementations: Go, C, and browser.

## 2. Packet-loss parity

A complete C build uses libdatachannel's C API for the exact v2 packet test. It:

1. obtains short-lived ICE/TURN credentials from `/api/turn/credentials`;
2. creates a relay-only peer connection and an unordered, unreliable data
   channel named `packet-loss`;
3. exchanges SDP through `/api/packet-test/offer`;
4. sends 1,000 exact 1,200-byte `NSPL` version-1 probe frames at 10 ms spacing;
5. validates every acknowledgement's magic, version, type, sequence, declared
   length, and deterministic padding;
6. reports client-observed transaction counters and RTT statistics;
7. reconciles those values with the daemon's authoritative forward-receive,
   acknowledgement-send, duplicate, invalid-frame, and send-failure counters;
8. reports transaction loss, forward loss, and reverse-acknowledgement loss as
   separate values.

`WEBRTC=no` is an explicit reduced build for platforms or build environments
without libdatachannel. Throughput and latency remain protocol-v2 compatible,
but packet loss is marked unavailable. Official C release binaries must use
`WEBRTC=yes`; compilation without WebRTC is not sufficient for release
qualification.

## 3. CLI and result parity

The C CLI supports the Go client's principal execution and output controls:

```text
--server URL / positional URL
--token TOKEN / NETSPEED_TOKEN
--json
--csv
--quiet
--verbose
--quick
--download-only
--upload-only
--no-packet-loss
--provider auto|netspeed|cloudflare
--download-payload auto|random|zero
--download-framing auto|fixed|chunked
--download-chunk-bytes N
--download-flush auto|true|false
--no-color
--timeout DURATION
--version
--help
```

Conflicting direction-only flags and conflicting machine-output modes are
rejected. Machine-readable failures are JSON-escaped when `--json` is active.

Successful JSON contains the same top-level model:

```text
meta
summary
quality
testConfidence
throughputSamples
latencySamples
packetLoss
startTime
endTime
```

Packet fields use the same directional names as the Go client, including
`forwardLossPercent`, `acknowledgementsSent`,
`acknowledgementsReceived`, and `reverseAcknowledgementLossPercent`.

Strict Netspeed JSON includes `meta.measurementCapabilities` and the normalized
`meta.measurementSelection`. Each latency sample includes `connectionReused`,
`probeTransport`, `probeMethod`, and `probePath`; WebSocket samples add
`webSocketProtocol`, while HTTP samples after a WebSocket failure add
`probeFallbackReason`.

Cloudflare compatibility uses the `cloudflare-http-v2` result contract. Before
measurement, the C client fetches a bounded 64 KiB body through only the common
`bytes` discriminator and classifies the provider-default payload and framing.
Explicit transport options are enforced only when that probe or exact response
headers prove the endpoint already satisfies them; the client never sends
Netspeed-only discriminator keys to an unadvertised endpoint. Later downloads
must preserve the observed behavior. Cloudflare latency uses dedicated
single-connection libcurl sessions, discards cold attempts, and reports warmup,
discarded-cold, server-timing adjustment, and HTTP protocol evidence under the
same field names as the Go adapter.

## 4. Portable builds

The repository root and `netspeed.c/Makefile` intentionally use the common GNU
make and BSD pmake subset. Platform and dependency detection is delegated to
POSIX shell rather than GNU make functions or conditionals.

Build all supported local programs:

```sh
make
```

Build only the Go client and daemon:

```sh
make go
```

Build the complete C client when libdatachannel is installed:

```sh
make c-client WEBRTC=yes
```

Build and run native-client tests without libdatachannel:

```sh
make c-check
make c-sanitize
make test-parity WEBRTC=no
```

Run the exact-frame unit suite with libdatachannel required:

```sh
make c-protocol-check WEBRTC_STATIC=yes
```

On OpenBSD and other BSD systems, use base `make`; `gmake` is not required.
The same files also work when that implementation is installed as `pmake`:

```sh
pmake go
pmake c-client WEBRTC=no
pmake test WEBRTC=no
```

The C subtree can also be driven independently:

```sh
pmake -C netspeed.c WEBRTC=no test
```

The `make-portability-check` target rejects GNU-only make conditionals,
functions, directives, pattern rules, and order-only prerequisites. OpenBSD CI
then parses and executes the Makefiles with actual pmake.

### C build variables

| variable | meaning | default |
|---|---|---|
| `CC` | C compiler | `cc` |
| `CFLAGS` | optimization/debug flags | `-O2` |
| `WARNFLAGS` | warning policy | `-Wall -Wextra -Wpedantic -Werror` |
| `WEBRTC` | `auto`, `yes`, or `no` | `auto` |
| `WEBRTC_STATIC` | request static libdatachannel link metadata | `no` |
| `PKG_CONFIG` | pkg-config executable | `pkg-config` |
| `VERSION` | reported version | `dev` |
| `COMMIT` | reported commit | `unknown` |
| `SOURCE_DATE` | deterministic build date | `unknown` |
| `PREFIX` | install prefix | `/usr/local` |

The build requires zlib for bounded Cloudflare payload classification and
discovers libcurl through pkg-config or `curl-config`.
libdatachannel is discovered through pkg-config, explicit
`WEBRTC_CFLAGS`/`WEBRTC_LIBS`, or conventional POSIX prefixes.

## 5. Qualification boundary

The native client release gate requires:

- strict GCC and Clang builds;
- protocol/statistics/frame unit tests;
- process tests against a protocol-v2 HTTP fixture with nonstandard advertised
  endpoint paths and query names;
- strict negative tests for old metadata, unsafe or unsupported capabilities,
  truncated downloads, incorrect upload receipts, encoded responses,
  transport-header mismatches, missing anti-transform controls, and connections
  that cannot be warmed;
- Cloudflare process tests for behavioral payload/framing selection, accepted
  and rejected explicit controls, discriminator-query suppression, compression
  rejection, anti-transform drift, and warm connection reuse;
- AddressSanitizer and UndefinedBehaviorSanitizer runs;
- a real libdatachannel build;
- real C-client to Pion-daemon interoperability through embedded TURN;
- actual OpenBSD base-make parsing and native build coverage;
- inclusion of the qualified C executable in the matching release archive;
- deterministic release reconstruction with the C binary held byte-identical.

The real TURN test is:

```sh
NETSPEED_C_CLIENT="$PWD/bin/netspeed-c" \
NETSPEED_E2E_TURN=1 \
make integration-c-turn
```

A release is not described as C-client-qualified until this workflow passes for
the exact tagged commit. Local builds that cannot obtain libdatachannel can
still qualify the HTTP measurement core, but they cannot establish packet-loss
or full release parity by themselves.
