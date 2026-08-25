netspeed
========

A self-hosted network speed test that measures download, upload, unloaded and
loaded latency, jitter, and directional packet loss.

It has three parts:

1. **netspeedd** — Go measurement daemon, WebRTC peer, and optional TURN relay;
2. **netspeed** — command-line client with human, quiet, CSV, and JSON output;
3. **web UI** — browser client with fixed-duration measurements and quality views.

The module requires Go 1.21.3 or newer.

This archive includes the completed Phase 1 measurement-integrity, Phase 2
measurement-methodology, Phase 3 WebRTC-lifecycle, Phase 4 service-hardening,
and Phase 5 HTTP/deployment work. The canonical contracts are:

- [`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md) — verified transfers,
  fixed-duration windows, loaded-latency overlap, and packet frames;
- [`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md) — session ownership, cancellation,
  disconnect recovery, and teardown;
- [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md) — admission limits,
  authentication, trusted proxies, TURN defaults, quotas, and metrics;
- [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md) — endpoint deadlines, browser API
  routing, CORS/Resource Timing, TLS, configuration, GeoIP, and shutdown;
- [`IMPROVEMENT_PHASES.md`](IMPROVEMENT_PHASES.md) — completed and remaining work.

quick start
-----------

```bash
# build the daemon and CLI
go build -o netspeedd ./cmd/netspeedd
go build -o netspeed ./cmd/netspeed

# serve the included web UI on HTTP localhost
./netspeedd -web-dir ./web

# open http://localhost:8080 or run the CLI
./netspeed http://localhost:8080
```

A normal Phase 2 client requires a Netspeed measurement-protocol-v2 server.
Legacy endpoints without verified upload receipts can only be used for an
explicit download-only test.

running the daemon
------------------

```bash
# basic local service
./netspeedd

# public HTTP service behind an existing TLS reverse proxy
./netspeedd \
  -listen 127.0.0.1:8080 \
  -hostname speed.example.com \
  -colo PDX \
  -web-dir ./web

# direct TLS mode
./netspeedd \
  -listen :443 \
  -tls-cert /path/to/cert.pem \
  -tls-key /path/to/key.pem
```

Flags override environment values. Run `./netspeedd -h` for the complete flag
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
./netspeedd -web-dir ./web
```

The CLI sends the token with every service request:

```bash
NETSPEED_TOKEN="$NETSPEEDD_ACCESS_TOKEN" ./netspeed https://speed.example.com
# equivalent:
./netspeed --token "$NETSPEEDD_ACCESS_TOKEN" https://speed.example.com
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
./netspeedd \
  -listen 127.0.0.1:8080 \
  -trusted-proxies '127.0.0.0/8,10.0.0.0/8'
```

`-trust-proxy` alone is insufficient and fails validation without trusted CIDRs.
This prevents arbitrary clients from spoofing the identity used for quotas,
rate limits, WebRTC ownership, metadata, and logs.

service limits
--------------

Phase 4 applies immediate admission controls rather than queueing timed
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
NETSPEEDD_TURN_SERVERS='stun:stun.example.com:3478' ./netspeedd
```

The bundled packet-test clients force TURN relay and report packet loss as
unavailable for STUN-only configuration; custom direct-ICE clients can use the
returned STUN list. External TURN URLs require `NETSPEEDD_TURN_SECRET`.
Credentials are random,
short-lived, and capped at 600 seconds by default.

Embedded TURN is opt-in and loopback-only by default:

```bash
./netspeedd -embedded-turn -embedded-turn-addr 127.0.0.1:3478
```

A non-loopback relay listener requires an explicit advertised IP and has a
combined inbound/outbound UDP rate ceiling:

```bash
NETSPEEDD_TURN_SECRET='replace-with-at-least-16-random-bytes' \
  ./netspeedd \
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
  ./netspeedd

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
./netspeed

# another protocol-v2 daemon
./netspeed https://speed.example.com

# quick test
./netspeed --quick

# scriptable output
./netspeed --json
./netspeed --csv

# detailed or compact terminal output
./netspeed --verbose
./netspeed --quiet

# explicit legacy download-only mode
./netspeed --download-only --no-packet-loss https://legacy.example.com
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
| `--no-color` | disable terminal colors |
| `-t, --timeout` | overall test timeout, default 60 seconds |

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
│   ├── clientaddr/      # trusted-proxy client identity
│   ├── config/          # configuration and validation
│   ├── limits/          # concurrency, byte quota, and token buckets
│   ├── locations/       # server location data
│   ├── measurement/     # shared measurement planning/statistics
│   ├── meta/            # client metadata and GeoIP
│   ├── protocol/        # verified upload and packet-frame protocol
│   ├── server/          # HTTP routes, security, and metrics
│   ├── telemetry/       # cross-package operational snapshots
│   ├── turn/            # optional bounded embedded TURN relay
│   └── webrtc/          # packet-test session manager
├── tests/web/           # dependency-free browser-engine tests
├── web/                 # browser UI
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
