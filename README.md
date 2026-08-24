netspeed
========

a self-hosted network speed test. measures download, upload, latency, jitter, and packet loss.

comes in three parts:

1. **netspeedd** - a go backend that handles the actual measurements
2. **netspeed** - a command-line client with ascii spinners and progress bars
3. **web ui** - a slick browser interface with dark/light mode

inspired by speed.cloudflare.com but you run it yourself.

requires **Go 1.23.2 or newer**. The current phased repair status and remaining work are tracked in [`IMPROVEMENT_PLAN.md`](IMPROVEMENT_PLAN.md).

---

quick start
-----------

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

the cli client works with your own netspeedd server or cloudflare's:

```bash
# test against your server
./netspeed https://speed.example.com

# test against cloudflare (default)
./netspeed

# quick test (fewer samples)
./netspeed --quick

# json output for scripting
./netspeed --json

# verbose mode with detailed breakdown
./netspeed -v

# quiet mode (just the numbers)
./netspeed --quiet
# outputs: download_mbps  upload_mbps  latency_ms  loss_percent
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

- **download speed** - how fast you can pull data. the cli honors the server-advertised ceiling; the phase-1 browser is temporarily capped at 100mb per transfer.
- **upload speed** - how fast you can push data. the cli streams generated request bodies; the phase-1 browser is temporarily capped at 50mb per transfer.
- **latency** - round-trip time to the server
- **loaded latency** - currently present, but guaranteed sustained-load overlap is the explicit phase-2 repair and is not yet release-qualified.
- **jitter** - variation in latency
- **packet loss** - currently uses webrtc datachannel round trips. exact-size, directional packet-loss semantics are the explicit phase-3 repair.

phase 1 adds a verifiable measurement contract: `/meta` advertises the protocol version and transfer ceiling, measurement-api-v1 uploads return the accepted byte count and server body-read duration, downloads require exact byte counts, and both clients reject non-200 responses. unavailable measurements are shown as `N/A`/`null`; they are never treated as zero. legacy third-party upload endpoints remain bounded but cannot provide the v1 server receipt.

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
│   ├── measurement/     # shared transfer protocol and validation
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

if you want to serve the ui through nginx or another frontend:

```bash
# run daemon without web-dir
./netspeedd -listen :8080

# serve web/ and reverse-proxy /meta, /locations, /__down, /__up,
# /api/*, and /cdn-cgi/* to netspeedd on the same public origin
```

the current ui uses relative urls, so a same-origin reverse proxy is required. configurable cross-origin api routing is scheduled for phase 6.

---

tests
-----

```bash
go test ./...
node web/js/speedtest.test.js
```

---

license
-------

see LICENSE file.

---

links
-----

- [github](https://github.com/yellowman/netspeed)
