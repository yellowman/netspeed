netspeed
========

a self-hosted network speed test. measures download, upload, latency, jitter, and packet loss.

comes in three parts:

1. **netspeedd** - a go backend that handles the actual measurements
2. **netspeed** - a command-line client with ascii spinners and progress bars
3. **web ui** - a slick browser interface with dark/light mode

inspired by speed.cloudflare.com but you run it yourself.

---

quick start
-----------

requires Go 1.21.3 or newer.

This archive contains the completed Phase 1 measurement-integrity and Phase 2
measurement-methodology work. See [`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md)
for the current measurement contract and [`IMPROVEMENT_PHASES.md`](IMPROVEMENT_PHASES.md)
for the remaining WebRTC, service-hardening, deployment, and release-qualification phases.

```bash
# build the daemon
go build -o netspeedd ./cmd/netspeedd

# build the cli client
go build -o netspeed ./cmd/netspeed

# run the daemon with the web ui
./netspeedd -web-dir ./web

# open http://localhost:8080 in your browser
# or use the cli client
./netspeed http://localhost:8080
```

---

running the daemon
------------------

```bash
# basic usage
./netspeedd

# with options
./netspeedd \
  -listen :8080 \
  -hostname speed.example.com \
  -colo NYC \
  -web-dir ./web

# tls mode
./netspeedd \
  -tls-cert /path/to/cert.pem \
  -tls-key /path/to/key.pem
```

you can also use environment variables:

```bash
export NETSPEEDD_LISTEN_ADDR=:443
export NETSPEEDD_HOSTNAME=speed.example.com
export NETSPEEDD_COLO=NYC
export NETSPEEDD_WEB_DIR=./web
./netspeedd
```

---

configuration
-------------

| flag | env var | description |
|------|---------|-------------|
| `-listen` | `NETSPEEDD_LISTEN_ADDR` | address to listen on (default `:8080`) |
| `-hostname` | `NETSPEEDD_HOSTNAME` | hostname shown in results |
| `-colo` | `NETSPEEDD_COLO` | datacenter code (iata style, like `JFK`) |
| `-web-dir` | `NETSPEEDD_WEB_DIR` | path to web ui files |
| `-tls-cert` | `NETSPEEDD_TLS_CERT` | tls certificate file |
| `-tls-key` | `NETSPEEDD_TLS_KEY` | tls key file |
| `-locations` | `NETSPEEDD_LOCATIONS_FILE` | json file with server locations |
| `-trust-proxy` | `NETSPEEDD_TRUST_PROXY` | trust x-forwarded-for headers |
| `-cors` | `NETSPEEDD_ENABLE_CORS` | enable cors (default true) |

for packet loss testing via webrtc, you'll also want:

| flag | env var | description |
|------|---------|-------------|
| `-turn-secret` | `NETSPEEDD_TURN_SECRET` | turn server shared secret |
| `-turn-servers` | `NETSPEEDD_TURN_SERVERS` | turn server urls (comma-separated) |
| `-turn-realm` | `NETSPEEDD_TURN_REALM` | turn realm |

---

using the cli client
--------------------

the cli client defaults to a local netspeedd server:

```bash
# test against the local daemon
./netspeed

# test against another netspeedd server
./netspeed https://speed.example.com

# legacy servers without verified upload receipts are download-only
./netspeed --download-only --no-packet-loss https://speed.cloudflare.com

# quick test (fewer samples)
./netspeed --quick

# json output for scripting
./netspeed --json

# verbose mode with detailed breakdown
./netspeed -v

# quiet mode (just the numbers)
./netspeed --quiet
# outputs: download_mbps  upload_mbps  latency_ms  loss_percent (or n/a)
```

**cli flags:**

| flag | description |
|------|-------------|
| `-s, --server` | server url |
| `-q, --quick` | quick test (fewer samples) |
| `-j, --json` | output as json |
| `--csv` | output as csv |
| `-v, --verbose` | detailed output |
| `--quiet` | minimal output |
| `-d, --download-only` | skip upload tests |
| `-u, --upload-only` | skip download tests |
| `--no-packet-loss` | skip packet loss test |
| `--no-color` | disable colors |
| `-t, --timeout` | test timeout (default 60s) |

---

what it measures
----------------

- **download speed** - three bounded, fixed-duration windows after small baseline probes
- **upload speed** - three bounded windows, with every request verified by an exact server receipt
- **latency** - unloaded round-trip time using precise request/first-byte timing
- **loaded latency** - probes accepted only when continuous download or upload traffic spans the entire probe
- **jitter** - p90 latency minus median latency after warmup removal and conservative IQR filtering
- **packet loss** - exact 1,200-byte WebRTC frames with transaction, forward, and reverse-acknowledgement loss reported separately
- **confidence** - explicit gates for sample count, variability, overlap, timing, and packet-test completion

the ui grades your connection for:
- video streaming
- online gaming
- video chatting

---

project structure
-----------------

```
netspeed/
├── cmd/netspeedd/       # main entry point
├── internal/
│   ├── config/          # configuration handling
│   ├── server/          # http server and handlers
│   ├── meta/            # client metadata extraction
│   ├── locations/       # server location data
│   └── webrtc/          # packet loss testing
├── web/                 # browser ui
│   ├── index.html
│   ├── css/styles.css
│   └── js/
│       ├── app.js       # main ui logic
│       ├── speedtest.js # measurement engine
│       └── charts.js    # visualizations
└── configs/             # example configs
```

---

running the ui separately
-------------------------

if you want to serve the ui from somewhere else (nginx, cdn, whatever):

```bash
# run daemon without web-dir
./netspeedd -listen :8080

# serve web/ from your preferred static server
# just make sure cors is enabled on the daemon
```

The current browser client uses relative API paths. Serve it from `netspeedd` or put
all daemon routes behind the same-origin reverse proxy. A configurable cross-origin API
base and complete CORS/timing-exposure contract remain Phase 5 work.

---

license
-------

see LICENSE file.

---

links
-----

- [github](https://github.com/yellowman/netspeed)
