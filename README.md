netspeed
========

A self-hosted network speed test that measures download, upload, unloaded and
loaded latency, jitter, and directional packet loss.

It has four parts:

1. **netspeedd** — Go measurement daemon, WebRTC peer, and optional TURN relay;
2. **netspeed** — Go command-line client with human, quiet, CSV, and JSON output;
3. **netspeed-c** — independent native C client implementing the same protocol-v2
   methodology and result model;
4. **web UI** — browser client with fixed-duration measurements and quality views.

The module requires Go 1.21.3 or newer. Release CI keeps that minimum-
version test while pinning the release compiler to Go 1.27.0.

capabilities
------------

### measurement integrity

- Measurement protocol v2 publishes explicit server capabilities and prevents
  clients from combining incompatible measurement methods with older servers.
- Downloads must return the expected status, content type, and exact byte count.
  The daemon can discriminate pseudorandom versus zero-fill payloads and fixed
  versus streamed framing without weakening exact-byte verification. Uploads
  return a bounded receipt confirming the exact accepted byte count and server
  body-read duration.
- Transfer planning obeys the daemon's advertised byte and concurrency ceilings.
  Go and C uploads are generated as streams, and browser transfer fallbacks are
  memory-bounded.
- Failed or incomplete measurements cannot satisfy sample requirements.
  Unavailable values remain `null` or `N/A` instead of becoming valid-looking
  zeroes.

### throughput, latency, and packet loss

- Small verified baselines select bounded chunks and reusable concurrent flows
  for fixed-duration throughput windows; high-rate tests do not depend on giant
  requests or allocations.
- A dedicated zero-body `/__ping` path supports warm keep-alive HTTP latency;
  loaded-latency probes are accepted only when continuous directional traffic
  spans the complete probe interval without a zero-load gap.
- Packet testing uses exact 1,200-byte binary frames and reports transaction,
  forward, and reverse-acknowledgement loss separately.
- Go, C, and browser clients use the same percentile, filtering, jitter,
  variability, confidence, and unavailable-value definitions.

### clients and user interfaces

- The Go CLI and native C CLI support human, quiet, CSV, and JSON output plus
  quick, download-only, upload-only, and packet-test controls.
- The C client is a supported peer of the Go client. A complete build uses
  libdatachannel for relay-only packet testing and daemon-counter
  reconciliation.
- The browser UI supports streamed downloads, streamed uploads where available,
  bounded fallbacks, quality views, a configurable API base URL, and explicit
  credential handling.
- Three browser presentations share the same measurement engine and result
  contract: [`web/index.html`](web/index.html) is the standard interface,
  [`web/alternate.html`](web/alternate.html) is a progressive observatory with
  a live test-sequence rail and visible evidence ledger, and
  [`web/phosphor.html`](web/phosphor.html) is a monochrome green-phosphor
  terminal interface.

### daemon, WebRTC, and operational safety

- WebRTC sessions have synchronized lifecycle ownership, cancellable offer
  negotiation, disconnect recovery, concurrency-safe teardown, and shutdown
  that waits for detached resource closure.
- Global and per-client transfer limits, byte quotas, session ceilings, and
  signaling rates reject overload before timed measurement work is queued.
- Optional bearer authentication protects measurement and control routes.
  Authenticated `/locations` responses use `private, no-store` and vary on
  `Authorization`, preventing shared-cache reuse across the access boundary.
- Forwarded client addresses are trusted only through configured proxy CIDRs.
  JSON control bodies are type-checked, size-bounded, and restricted to one
  value without trailing data.
- Embedded TURN is opt-in, loopback-only by default, startup-validated, and
  bandwidth-limited. Prometheus metrics are opt-in and separately protectable.

### HTTP and deployment

- Header, control-request, transfer, and idle deadlines are independent, so
  slow measurement bodies are not truncated by a whole-request timeout.
- Measurement responses use `no-store, no-transform`, CDN cache suppression,
  and `X-Accel-Buffering: no`; CORS, Resource Timing, credentialed browser access,
  direct TLS, reverse-proxy identity, and graceful shutdown have explicit
  validated behavior.
- Configuration uses command-line flags and strictly parsed `NETSPEEDD_*`
  environment variables. ASN and City MaxMind databases can be configured
  independently.

### builds and release qualification

- Root and C Makefiles use the common GNU make and BSD pmake subset and build
  `netspeed`, `netspeedd`, and `netspeed-c` through the same public targets.
- CI covers the real dependency graph, Go 1.21.3 compatibility, race detection,
  vet, static and vulnerability analysis, browser automation, strict GCC/Clang
  C builds, real WebRTC/TURN interoperability, Windows, and OpenBSD.
- Release archives are deterministic, checksummed, reconstructed twice for
  byte-for-byte comparison, and protected by source-hygiene checks that reject
  stale binaries, oversized files, module replacements, and broken local links.

web interfaces
--------------

All three browser presentations use the same production measurement engine and
result contract. The screenshots below use one representative completed result
so the interface designs can be compared directly. Each image links to its
corresponding HTML file.

### standard

[![Standard Netspeed interface showing throughput, latency, jitter, and packet-loss results](web/screenshots/standard.png)](web/index.html)

### observatory

[![Netspeed Observatory interface with progressive test sequence and evidence-first layout](web/screenshots/observatory.png)](web/alternate.html)

### phosphor

[![Netspeed green-phosphor terminal interface inspired by an Apple II system monitor](web/screenshots/phosphor.png)](web/phosphor.html)

The repository is defined by the following canonical contracts:

- [`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md) — verified transfers,
  fixed-duration windows, loaded-latency overlap, and packet frames;
- [`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md) — session ownership, cancellation,
  disconnect recovery, and teardown;
- [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md) — admission limits,
  authentication, trusted proxies, TURN defaults, quotas, and metrics;
- [`HTTP_MEASUREMENT_TRANSPORT.md`](HTTP_MEASUREMENT_TRANSPORT.md) — HTTP
  payload, framing, latency, compression, caching, and proxy-buffer controls;
- [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md) — endpoint deadlines, browser API
  routing, CORS/Resource Timing, TLS, configuration, GeoIP, and shutdown;
- [`RELEASE_QUALIFICATION.md`](RELEASE_QUALIFICATION.md) — CI, end-to-end,
  supported-platform, source-hygiene, and deterministic-release gates;
- [`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md) — Go/C measurement, output, build,
  packet-test, and qualification parity.

quick start
-----------

```bash
# build the daemon, Go CLI, and native C CLI
make

# serve the included web UI on HTTP localhost
./bin/netspeedd -web-dir ./web

# open http://localhost:8080 or run the CLI
./bin/netspeed http://localhost:8080
# equivalent native client
./bin/netspeed-c http://localhost:8080
```

A normal client requires a Netspeed measurement-protocol-v2 server.
Legacy endpoints without verified upload receipts can only be used for an
explicit download-only test.

development and release qualification
-------------------------------------

The common local checks are exposed through the top-level Makefile:

```bash
make fmt-check
make hygiene
make docs-check
make make-portability-check
make workflow-contract-check
make release-tools
make test
make test-parity
make race
make vet
make web-test
make c-check
make c-sanitize
make integration
```

The full non-platform release gate is available as `make ci`; it deliberately
requires the analyzers, Chromium, libdatachannel, embedded-TURN fixtures,
sanitizers, and double-build reproducibility check. GitHub Actions adds native
Windows and OpenBSD jobs, and the publishing job cannot run unless every
blocking job succeeds. See
[`RELEASE_QUALIFICATION.md`](RELEASE_QUALIFICATION.md).

All command-line programs use injected version, commit, and source-date
metadata:

```bash
./bin/netspeed --version
./bin/netspeedd --version
./bin/netspeed-c --version
```

Official binary archives are generated for Linux, OpenBSD, and Windows on amd64
and arm64. `scripts/release.py` also emits a deterministic source ZIP,
`SHA256SUMS`, and `release-manifest.json`. A qualified C executable is included
in each platform archive for which the release workflow supplies one; the
current publishing workflow requires the complete `WEBRTC=yes` Linux/amd64 C
client rather than silently shipping an HTTP-only build.

The C implementation is a supported protocol-v2 client. Its release gate adds
strict GCC/Clang builds, sanitizers, negative transfer-contract tests, and real
libdatachannel-to-Pion/TURN interoperability. See
[`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md).

running the daemon
------------------

```bash
# basic local service
./bin/netspeedd

# public HTTP service behind an existing TLS reverse proxy
./bin/netspeedd \
  -listen 127.0.0.1:8080 \
  -hostname speed.example.com \
  -colo PDX \
  -web-dir ./web

# direct TLS mode
./bin/netspeedd \
  -listen :443 \
  -tls-cert /path/to/cert.pem \
  -tls-key /path/to/key.pem
```

Flags override environment values. Run `./bin/netspeedd -h` for the complete flag
list. The daemon uses a 10-second header timeout, 30-second control-request
timeout, five-minute per-transfer timeout, and two-minute keep-alive idle
timeout by default. It does not apply a whole-request `ReadTimeout` or
`WriteTimeout` to slow measurement bodies.

Configuration is flags plus `NETSPEEDD_*` environment variables. There is no
YAML loader. A complete example is
[`configs/netspeedd.env.example`](configs/netspeedd.env.example).

### protected deployment

A shared bearer token can protect the measurement and packet-test API:

```bash
export NETSPEEDD_ACCESS_TOKEN='replace-with-at-least-16-random-bytes'
export NETSPEEDD_MAX_CONCURRENT_TRANSFERS=128
export NETSPEEDD_MAX_CONCURRENT_TRANSFERS_PER_CLIENT=12
export NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_BYTES=$((250 * 1024 * 1024 * 1024))
export NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_WINDOW=1h
./bin/netspeedd -web-dir ./web
```

The CLI sends the token with every service request:

```bash
NETSPEED_TOKEN="$NETSPEEDD_ACCESS_TOKEN" ./bin/netspeed https://speed.example.com
# equivalent:
./bin/netspeed --token "$NETSPEEDD_ACCESS_TOKEN" https://speed.example.com
```

The browser engine reads a configuration object defined before `speedtest.js`:

```html
<script>
  globalThis.NETSPEED_CONFIG = {
    // Omit apiBaseUrl for same-origin deployment.
    apiBaseUrl: "https://speed-api.example.com/",
    credentials: "omit",
    accessToken: "replace-with-a-deployment-token"
  };
</script>
<script src="js/speedtest.js"></script>
```

A browser-visible token is not secret from that browser. Use this only for a
controlled installation or inject a short-lived token through an authenticated
upstream application. For a separate UI origin, configure the matching daemon
CORS origin; use `credentials: "include"` only with explicit credentialed CORS.
See [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md).

### trusted reverse proxy

Forwarding headers are ignored by default. Configure every trusted proxy hop as
a CIDR:

```bash
./bin/netspeedd \
  -listen 127.0.0.1:8080 \
  -trusted-proxies '127.0.0.0/8,10.0.0.0/8'
```

`-trust-proxy` alone is insufficient and fails validation without trusted CIDRs.
This prevents arbitrary clients from spoofing the identity used for quotas,
rate limits, WebRTC ownership, metadata, and logs.

service limits
--------------

The daemon applies immediate admission controls rather than queueing timed
measurement work.

| control | default |
|---|---:|
| one transfer | 1 GiB maximum |
| active transfers | 256 global / 24 per client |
| byte quota | 1 TiB per client per hour |
| active WebRTC sessions | 64 global / 2 per client |
| WebRTC offer rate | 12/minute, burst 4 |
| ICE/TURN configuration rate | 60/minute, burst 10 |
| offer/report JSON bodies | 128 KiB / 16 KiB |
| embedded TURN | disabled |
| metrics | disabled and token-required |

`/meta` advertises the per-client transfer ceiling. The CLI and browser scale
their flow counts down and reserve one slot for loaded-latency probes.

See [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md) for all flags, environment
variables, status codes, quota semantics, and operational limitations.

ICE, STUN, and TURN
-------------------

A STUN-only server list does not require a shared secret:

```bash
NETSPEEDD_TURN_SERVERS='stun:stun.example.com:3478' ./bin/netspeedd
```

The bundled packet-test clients force TURN relay and report packet loss as
unavailable for STUN-only configuration; custom direct-ICE clients can use the
returned STUN list. External TURN URLs require `NETSPEEDD_TURN_SECRET`.
Credentials are random,
short-lived, and capped at 600 seconds by default.

Embedded TURN is opt-in and loopback-only by default:

```bash
./bin/netspeedd -embedded-turn -embedded-turn-addr 127.0.0.1:3478
```

A non-loopback relay listener requires an explicit advertised IP and has a
combined inbound/outbound UDP rate ceiling:

```bash
NETSPEEDD_TURN_SECRET='replace-with-at-least-16-random-bytes' \
  ./bin/netspeedd \
  -embedded-turn \
  -embedded-turn-addr 0.0.0.0:3478 \
  -embedded-turn-ip 198.51.100.20 \
  -embedded-turn-max-mbps 100
```

Embedded TURN and user-configured external ICE URLs cannot be enabled together.

metrics
-------

The Prometheus endpoint is opt-in and requires either its own bearer token or
the service access token:

```bash
NETSPEEDD_ENABLE_METRICS=true \
NETSPEEDD_METRICS_TOKEN='replace-with-a-separate-metrics-token' \
  ./bin/netspeedd

curl -H 'Authorization: Bearer replace-with-a-separate-metrics-token' \
  http://127.0.0.1:8080/metrics
```

Counters cover HTTP activity, authentication failures, transfer/session
admission, quotas, measurement bytes, signaling, control-body rejection,
internal failures, and embedded TURN UDP traffic.

using the CLI
-------------

```bash
# local daemon
./bin/netspeed

# another protocol-v2 daemon
./bin/netspeed https://speed.example.com

# quick test
./bin/netspeed --quick

# scriptable output
./bin/netspeed --json
./bin/netspeed --csv

# detailed or compact terminal output
./bin/netspeed --verbose
./bin/netspeed --quiet

# explicit legacy download-only mode
./bin/netspeed --download-only --no-packet-loss https://legacy.example.com

# low-CPU streamed zero-fill against a capability-aware Netspeed daemon
./bin/netspeed --provider netspeed \
  --download-payload zero --download-framing chunked \
  --download-chunk-bytes 65536 --download-flush=false \
  https://speed.example.com
```

Important CLI flags:

| flag | description |
|---|---|
| `-s, --server` | server URL |
| `--token` | shared bearer token; falls back to `NETSPEED_TOKEN` |
| `-q, --quick` | reduced fixed-window test |
| `-j, --json` | JSON output |
| `--csv` | CSV output |
| `-v, --verbose` | detailed output |
| `--quiet` | compact numeric output |
| `-d, --download-only` | skip upload |
| `-u, --upload-only` | skip download |
| `--no-packet-loss` | skip WebRTC packet test |
| `--download-payload` | `auto`, `random`, or `zero`; negotiated by Netspeed or enforced as an observed-default constraint by the native Cloudflare adapters |
| `--download-framing` | `auto`, `fixed`, or `chunked`; Cloudflare mode never sends an unadvertised framing query |
| `--download-chunk-bytes` | application chunk size; `0` is automatic, while Cloudflare requires exact response-header evidence |
| `--download-flush` | `auto`, `true`, or `false`; Cloudflare requires exact response-header evidence |
| `--no-color` | disable terminal colors |
| `-t, --timeout` | overall test timeout, default 60 seconds |

The strict Go and native C clients validate `measurementCapabilities` before
using it, follow only same-origin relative endpoint paths, and record the
normalized choice as `meta.measurementSelection` in JSON output. Their
Cloudflare adapters instead behaviorally probe the common endpoint surface,
record the evidence in `httpTransport`, and treat explicit controls as
requirements on the observed provider defaults. They never send Netspeed-only
discriminator keys to an endpoint that did not advertise them.

what it measures
----------------

- **download speed** — three bounded, fixed-duration windows after verified
  baseline probes;
- **upload speed** — three bounded windows whose completed requests are checked
  against exact server receipts;
- **unloaded latency** — request-write to first-response-byte round trips;
- **loaded latency** — probes accepted only when continuous transfer traffic
  spans the complete probe interval;
- **jitter** — p90 latency minus median latency after warmup removal and
  conservative IQR filtering;
- **packet loss** — exact 1,200-byte WebRTC frames with transaction, forward,
  and reverse-acknowledgement loss reported separately;
- **confidence** — explicit gates for sample count, variability, overlap, timing,
  and directional packet accounting.

project structure
-----------------

```text
netspeed/
├── cmd/netspeed/        # Go CLI
├── cmd/netspeedd/       # daemon entry point
├── internal/
│   ├── buildinfo/       # shared linker-injected release identity
│   ├── clientaddr/      # trusted-proxy client identity
│   ├── config/          # configuration and validation
│   ├── limits/          # concurrency, byte quota, and token buckets
│   ├── locations/       # server location data
│   ├── measurement/     # shared measurement planning/statistics
│   ├── measurementhttp/ # HTTP transport capability and streaming contract
│   ├── meta/            # client metadata and GeoIP
│   ├── protocol/        # verified upload and packet-frame protocol
│   ├── server/          # HTTP routes, security, and metrics
│   ├── telemetry/       # cross-package operational snapshots
│   ├── turn/            # optional bounded embedded TURN relay
│   └── webrtc/          # packet-test session manager
├── scripts/             # source hygiene, portable-make, dependency, and release tooling
├── tests/
│   ├── browser/         # real Chromium smoke test
│   ├── integration/     # Go/C daemon and embedded-TURN process fixtures
│   ├── release/         # deterministic packager tests
│   └── web/             # dependency-free browser-engine tests
├── web/                 # browser UI
├── netspeed.c/          # first-class native C protocol-v2 client
├── C_CLIENT_PARITY.md   # Go/C feature and qualification contract
└── configs/             # locations and shell-compatible daemon env examples
```

running the UI separately
-------------------------

Same-origin deployment needs no browser configuration. For a separately hosted
UI, set `globalThis.NETSPEED_CONFIG.apiBaseUrl` before loading `speedtest.js` and
configure `NETSPEEDD_ALLOWED_ORIGINS` on the daemon. The API base may include a
path prefix. The daemon returns matching CORS and `Timing-Allow-Origin` headers,
and rejects browser requests from unapproved origins.

The complete proxy, credential, timeout, TLS, configuration, GeoIP, and shutdown
contract is in [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md). The daemon accepts
flags and `NETSPEEDD_*` environment variables; it intentionally has no YAML
loader. Start from [`configs/netspeedd.env.example`](configs/netspeedd.env.example).

license
-------

See `LICENSE`.

links
-----

- [GitHub repository](https://github.com/yellowman/netspeed)


### Cloudflare compatibility

The native clients support `--provider auto`, `--provider netspeed`, and
`--provider cloudflare`. `netspeed` preserves protocol-v2 metadata and verified
upload receipts. `cloudflare` uses the common Cloudflare HTTP surface and reports
upload evidence as client-observed, never as a Netspeed receipt. `auto` selects
Cloudflare only after a positive hostname or response-header fingerprint;
recognizable incompatible Netspeed metadata is never downgraded.

The Go and native C Cloudflare paths use the `cloudflare-http-v2` contract. They
probe and label the provider-default download payload and framing, disable HTTP
content decoding, send `no-store, no-transform`, reject encoded responses, and
report only warm latency samples whose keep-alive connection reuse was observed.
Explicit transport flags constrain the observed defaults; no Netspeed-only
discriminator query parameters are sent.

Cloudflare-compatible TURN loopback requires usable TURN credentials, supplied
with `--turn-credentials-url` or `--turn-url`, `--turn-username`, and
`--turn-credential`. The C build requires libdatachannel for this packet test;
without it the measurement is explicitly unavailable.

```sh
netspeed --provider auto --server https://speed.cloudflare.com
# Continue only if the provider-default body is demonstrably random.
netspeed --provider cloudflare --download-payload random \
  --server https://speed.cloudflare.com
netspeed --provider cloudflare --server https://example.test \
  --turn-credentials-url https://example.test/turn-credentials
netspeed-c --provider cloudflare --server https://example.test \
  --turn-url 'turn:turn.example.test:3478?transport=udp' \
  --turn-username USER --turn-credential PASS
```

Auto mode keeps the verified Netspeed protocol whenever the server identifies
itself as Netspeed and permits Cloudflare fallback only after a positive
Cloudflare compatibility fingerprint. WebRTC packet testing retains the
authoritative Netspeed server-peer topology and also supports
Cloudflare-compatible relay-only TURN loopback with two local peers. Results
identify the selected provider, measurement contract, and packet topology.

### Interface notes

- **Standard** is the compact general-purpose client.
- **Observatory** removes the promotional hero and opens directly on the instruments, evidence, loaded-latency, packet-delivery, and confidence views.
- **Phosphor** is an Apple II/ProDOS-inspired text-mode monitor: uppercase fixed-column typography, inverse-video headings, character plots and meters, scanlines, a block cursor, and a strictly monochrome green-phosphor display. It uses the same measurement engine and result elements as the other interfaces.
- The progressive rail consumes structured measurement outcomes. A skipped capability remains `unavailable`, a failed operation remains `failed`, and final analysis never blanket-marks earlier work successful.
- Presentation links retain the supported shared-result `r` parameter while discarding unrelated query state, so a shared measurement survives switching among Standard, Observatory, and Phosphor.

## Native client progress

The Go and C clients report live work to standard error when run in a terminal or with `-v`. Progress includes provider discovery, idle-latency probes, calibration transfers, sustained download and upload windows, loaded-latency overlap, and packet-path setup. Machine-readable JSON and CSV remain clean on standard output. Set `NETSPEED_PROGRESS=0` to suppress progress or `NETSPEED_PROGRESS=1` to force it when stderr is redirected.
