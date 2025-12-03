netspeed
========

a self-hosted network speed test. measures download, upload, latency, jitter, and packet loss.

comes in two parts:

1. **netspeedd** - a go backend that handles the actual measurements
2. **web ui** - a slick browser interface with dark/light mode

inspired by speed.cloudflare.com but you run it yourself.

---

quick start
-----------

```bash
# build the daemon
go build -o netspeedd ./cmd/netspeedd

# run it with the web ui
./netspeedd -web-dir ./web

# open http://localhost:8080 in your browser
# click "start test" and watch the magic happen
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

what it measures
----------------

- **download speed** - how fast you can pull data (tests 100kb to 100mb chunks)
- **upload speed** - how fast you can push data (tests 100kb to 50mb chunks)
- **latency** - round-trip time to the server
- **loaded latency** - latency while downloading/uploading (bufferbloat detection)
- **jitter** - variation in latency
- **packet loss** - uses webrtc datachannel to detect dropped packets

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

the ui talks to the daemon via fetch, so cross-origin works fine.

---

license
-------

see LICENSE file.

---

links
-----

- [github](https://github.com/yellowman/netspeed)
