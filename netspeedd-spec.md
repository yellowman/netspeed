netspeedd go-based speedtest backend
====================================

this document describes a go daemon that emulates the public api surface used by speed.cloudflare.com (based on cloudflare's published speedtest tooling and known public endpoints).

it covers:

- the endpoints:
  - `GET /meta`
  - `GET /__down`
  - `POST /__up`
  - `GET /__ws` (HTTP/1.1 WebSocket upgrade)
  - `GET` or `HEAD /__ping`
  - `GET /locations`
  - (optional) `GET /cdn-cgi/trace`
- request/response semantics
- configuration, interfaces, and internal structure for a production-ready go server

---


> **Implemented authority:** [`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md)
> is the canonical measurement contract,
> [`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md) is the canonical session contract,
> [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md) is the canonical
> deployment-safety contract,
> [`HTTP_MEASUREMENT_TRANSPORT.md`](HTTP_MEASUREMENT_TRANSPORT.md) is the
> canonical HTTP measurement-discriminator contract, and
> [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md) is the canonical HTTP/deployment
> contract. Those documents supersede incompatible
> legacy examples in this combined design note.

Service hardening normative addendum
------------------------------------

The implemented daemon applies bounded defaults even without authentication:
256 active transfers globally, 24 per resolved client, a 1 TiB/client/hour byte
quota, 64 active WebRTC sessions globally, two per client, token-bucket limits
for signaling and ICE configuration, and 128 KiB/16 KiB JSON body limits.
Overloaded measurement work is rejected immediately with `429` or `503`; it is
not queued.

`NETSPEEDD_ACCESS_TOKEN` optionally protects the measurement and `/api/`
surface with `Authorization: Bearer`. `/health` stays public. `/metrics` is
opt-in and requires its own token or the access token. Forwarded client
addresses are accepted only from `NETSPEEDD_TRUSTED_PROXY_CIDRS` and malformed
chains are ignored. Embedded TURN is disabled and loopback-only by default; a
non-loopback listener requires an explicit advertised IP and a positive UDP rate
ceiling. See `SERVICE_HARDENING.md` for the complete status and configuration
contract.

HTTP and deployment normative addendum
--------------------------------------

The implemented daemon uses a header-only server timeout plus endpoint-specific
control and transfer deadlines; it does not use whole-request `ReadTimeout` or
`WriteTimeout`. The browser supports a validated `apiBaseUrl`, path prefixes,
one Fetch/XHR credentials mode, and strict negotiation of advertised measurement
paths and
discriminators. Allowed origins receive matching CORS and
`Timing-Allow-Origin` headers plus exposed content-coding and transport
diagnostics; disallowed browser origins receive `403`.

The daemon advertises pseudorandom and zero-fill payloads, fixed and streamed
framing, identity-only uploads, a persistent `netspeed.ping.v1` WebSocket echo,
and warm zero-body HTTP fallback. Measurement responses and the WebSocket
upgrade carry anti-transform and proxy-buffer-suppression headers; proxy
configuration is still required for request-body buffering and Upgrade
forwarding. The HTTP writer records only the first status and actual bytes,
delegates modern streaming, hijacking, and response-controller capabilities,
and aborts a committed stream after a panic instead of appending a replacement
error body. Graceful shutdown drains HTTP handlers before WebRTC, GeoIP, or
embedded TURN are closed.

Flags and strictly parsed `NETSPEEDD_*` environment variables are the canonical
configuration surface; there is no YAML loader. Partial or invalid direct-TLS
configuration fails before listening. ASN and City MaxMind databases are wired
independently and configured database failures are fatal. See
`HTTP_DEPLOYMENT.md` for the complete contract.

1. api surface
==============

1.1 core measurement endpoints
-----------------------------

### 1.1.1 `GET /meta` - client metadata

**purpose**

returns per-client metadata similar to `https://speed.cloudflare.com/meta`, typically including:

- hostname
- client ip
- http protocol
- asn and as organization
- colo (data center iata code, e.g., `JFK`)
- country, region, city, postal code
- latitude, longitude
- timezone (optional)

**example response**

```json
{
  "hostname": "speed.cloudflare.com",
  "clientIp": "203.0.113.42",
  "httpProtocol": "HTTP/2.0",
  "asn": 13254,
  "asOrganization": "Example ISP",
  "colo": "JFK",
  "country": "US",
  "city": "New York City",
  "region": "New York",
  "postalCode": "10001",
  "latitude": 40.73061,
  "longitude": -73.935242,
  "timezone": "America/New_York",
  "maxTransferBytes": 1073741824,
  "maxConcurrentTransfersPerClient": 24,
  "measurementProtocolVersion": 2,
  "uploadReceiptVersion": 1,
  "packetLossFrameVersion": 1,
  "measurementCapabilities": {
    "version": 1,
    "downloadPayloads": ["random", "zero"],
    "downloadFramings": ["fixed", "chunked"],
    "httpPingPath": "/__ping",
    "httpPingMethods": ["GET", "HEAD"],
    "webSocketPingPath": "/__ws",
    "webSocketPingProtocol": "netspeed.ping.v1",
    "webSocketPingPayloadBytes": 16
  }
}
```

**request**

- method: `GET`
- path: `/meta`
- query parameters: ignored (must tolerate arbitrary extras)
- body: none

**response**

- status: `200 OK`
- headers:
  - `Content-Type: application/json; charset=utf-8`
  - `Cache-Control: no-store`
- body: json as above

the exact field names are chosen for compatibility with the real `/meta` endpoint.

---

### 1.1.2 `GET /__down` - download / latency payload

The compatibility request is `GET /__down?bytes=N`. Transport-controls version
1 additionally accepts `payload=random|zero`, `framing=fixed|chunked`,
`chunkBytes=N`, and `flush=true|false`, while tolerating opaque correlation
parameters such as `measId` and `during`.

Fixed framing supplies `Content-Length: N`. Streamed framing deliberately omits
`Content-Length`, commits streaming before the first body write, and uses
application flushes according to `flush`. Both modes return exactly `N` bytes or
fail; clients never accept a short response. Pseudorandom mode generates a
nonrepeating per-request stream, while zero-fill minimizes generator CPU.

Every response carries `Cache-Control: no-store, no-transform`, CDN/surrogate
cache suppression, `X-Accel-Buffering: no`, and diagnostic
payload/framing/chunk/flush headers. The complete query, status, and response
contract is in
[`HTTP_MEASUREMENT_TRANSPORT.md`](HTTP_MEASUREMENT_TRANSPORT.md).

---

### 1.1.3 `POST /__up` - verified upload

The daemon reads the complete request body and distinguishes these outcomes:

- a declared length above `Config.MaxBytes` is rejected with `413`;
- an undeclared body that reaches `MaxBytes + 1` is rejected with `413`;
- a body shorter than its declared `Content-Length` is rejected with `400`;
- an optional `bytes=N` discriminator that does not match is rejected with `400`;
- gzip, Brotli, or any other non-identity `Content-Encoding` is rejected with
  `415`;
- any body-read failure is rejected with `400`.

A successful request returns JSON receipt version 1:

```json
{
  "ok": true,
  "acceptedBytes": 1000000,
  "serverDurationNs": 123456789
}
```

`acceptedBytes` is the exact number of bytes consumed. `serverDurationNs` spans
daemon body reading and is the canonical per-request upload duration. The daemon
sets JSON content type and `Cache-Control: no-store, no-transform`. It never
returns success for truncated or silently limited input.

### 1.1.4 `GET /__ws` - persistent WebSocket latency echo

The optional route is used only when `/meta` advertises the exact path,
`netspeed.ping.v1` subprotocol, and 16-byte message size. It requires an HTTP/1.1
RFC 6455 upgrade. Each accepted binary message is:

```text
NSP1 | uint32 sequence in network byte order | 8-byte random nonce
```

The daemon validates one unfragmented, masked 16-byte binary message and echoes
it unchanged. It answers WebSocket control pings with pongs and rejects text,
fragmented, oversized, malformed, or wrong-magic application messages. The 101
response selects the exact subprotocol and supplies `Cache-Control: no-store,
no-transform`, `Pragma: no-cache`, `X-Accel-Buffering: no`, and
`X-Netspeed-Measurement: latency`.

The connection consumes one global and per-client transfer slot until it closes
or reaches `TransferTimeout`, but its tiny echo messages are not charged against
the byte quota. Capable clients send one unreported warmup, measure only message
send to exact nonce echo, and permanently use the warm HTTP fallback after the
first upgrade or protocol failure. The complete wire contract is in
[`HTTP_MEASUREMENT_TRANSPORT.md`](HTTP_MEASUREMENT_TRANSPORT.md).

### 1.1.5 `GET` or `HEAD /__ping` - warm HTTP latency

The endpoint returns `200`, `Content-Length: 0`, and no body. Native clients
reuse one persistent HTTP connection. The browser warms its origin pool and uses
Resource Timing to reject observed connection setup or an unverifiable manual
fallback, while labeling hidden reuse evidence as unknown when the measured
request-start interval still excludes setup. `GET /__down?bytes=0` remains the
compatibility fallback. Older clients and any client whose WebSocket upgrade or
echo fails use this path for the rest of the run.

### 1.1.6 `GET /locations` - colo list

**purpose**

returns a list of test locations / data centers. this matches the public `https://speed.cloudflare.com/locations` shape, so any compatible ui can render a global map of possible test locations.

**request**

- method: `GET`
- path: `/locations`
- query parameters: ignored
- body: none

**response**

- status: `200 OK`
- headers:
  - `Content-Type: application/json; charset=utf-8`
  - `Cache-Control: public, max-age=86400` (or similar)
- body: json array of `Location` objects:

```json
[
  {
    "iata": "JFK",
    "lat": 40.6413,
    "lon": -73.7781,
    "cca2": "US",
    "region": "North America",
    "city": "New York"
  },
  {
    "iata": "LHR",
    "lat": 51.47,
    "lon": -0.4543,
    "cca2": "GB",
    "region": "Europe",
    "city": "London"
  }
]
```

---

1.2 optional diagnostic endpoint
--------------------------------

### 1.2.1 `GET /cdn-cgi/trace` (optional)

if you want to mimic a cf-like trace endpoint:

- method: `GET`
- path: `/cdn-cgi/trace`
- response:
  - `Content-Type: text/plain; charset=utf-8`
  - body: newline separated `key=value` pairs (subset is fine), e.g.:

    ```text
    ip=203.0.113.42
    tls=TLSv1.3
    http=http/2
    colo=JFK
    loc=US
    ```

this is not required for the speedtest itself; it’s just convenient for debugging.

---

2. go daemon design
===================

2.1 overview
------------

binary name: `netspeedd`

responsibilities:

- serve `/meta`, `/__down`, `/__up`, `/__ws`, `/__ping`, `/locations` (and optional `/cdn-cgi/trace`)
- expose simple configuration (listen address, tls, limits, cors, geo db, locations file)
- be robust under very high concurrency and long-lived connections

2.2 configuration model
-----------------------

```go
type Config struct {
    ListenAddr string

    TLSCertFile string
    TLSKeyFile  string
    MaxBytes    int64

    ReadHeaderTimeout time.Duration
    ControlTimeout    time.Duration
    TransferTimeout   time.Duration
    IdleTimeout       time.Duration

    EnableCORS           bool
    AllowedOrigins       []string
    CORSAllowCredentials bool

    LocationsFile          string
    GeoIPASNDatabasePath   string
    GeoIPCityDatabasePath  string
    TrustedProxyCIDRs      []string
    Hostname               string
    Colo                   string

    MaxConcurrentTransfers          int
    MaxConcurrentTransfersPerClient int
    ClientBandwidthQuotaBytes       int64
    ClientBandwidthQuotaWindow      time.Duration
    MaxWebRTCSessions               int
    MaxWebRTCSessionsPerClient      int
    WebRTCOfferRatePerMinute        int
    WebRTCOfferBurst                int
    TurnCredentialRatePerMinute     int
    TurnCredentialBurst             int
    MaxOfferBodyBytes               int64
    MaxReportBodyBytes              int64

    AccessToken   string
    MetricsToken  string
    EnableMetrics bool

    TurnSecret          string
    TurnServers         []string
    TurnRealm           string
    MaxTurnTTL          int64
    EmbeddedTurn        bool
    EmbeddedTurnAddr    string
    EmbeddedTurnPublicIP string
    EmbeddedTurnMaxMbps int64
    WebDir              string
}
```

The service controls are normative in `SERVICE_HARDENING.md`. Timeout,
CORS, TLS, configuration, GeoIP, and shutdown behavior are normative in
`HTTP_DEPLOYMENT.md`. Flags explicitly supplied on the command line override
strictly parsed `NETSPEEDD_*` environment values.

2.3 core interfaces
-------------------

### 2.3.1 meta provider

```go
type ClientMeta struct {
    Hostname      string  `json:"hostname"`
    ClientIP      string  `json:"clientIp"`
    HTTPProtocol  string  `json:"httpProtocol"`
    ASN           int     `json:"asn"`
    ASOrg         string  `json:"asOrganization"`
    Colo          string  `json:"colo"`
    Country       string  `json:"country"`
    City          string  `json:"city"`
    Region        string  `json:"region"`
    PostalCode    string  `json:"postalCode"`
    Latitude      float64 `json:"latitude"`
    Longitude     float64 `json:"longitude"`
    Timezone                   string  `json:"timezone,omitempty"`
    MaxTransferBytes                int64 `json:"maxTransferBytes"`
    MaxConcurrentTransfersPerClient int   `json:"maxConcurrentTransfersPerClient"`
    MeasurementProtocolVersion      int   `json:"measurementProtocolVersion"`
    UploadReceiptVersion       int     `json:"uploadReceiptVersion"`
    PacketLossFrameVersion     int     `json:"packetLossFrameVersion"`
}

type MetaProvider interface {
    MetaFor(r *http.Request) ClientMeta
}
```

**implementations**

1. `StaticMetaProvider`
   - returns fixed values (handy for testing).

2. `CityGeoIPProvider`
   - opens `GeoIPASNDatabasePath` and `GeoIPCityDatabasePath` independently;
   - the ASN database supplies ASN and organization;
   - the City database supplies country, subdivision, city, postal code,
     coordinates, and timezone;
   - either database may be configured alone;
   - a configured database that cannot open fails startup;
   - absent City data remains unknown rather than defaulting to a country.

3. Trusted proxy client-address resolution
   - forwarding headers are ignored unless the direct peer belongs to an explicitly configured proxy CIDR;
   - `X-Forwarded-For` is walked from the trusted edge inward and a malformed chain is rejected as a whole;
   - single-IP headers such as `CF-Connecting-IP` and `X-Real-IP` are accepted only from a trusted direct peer.

the daemon selects the implementation based on config at startup.

---

### 2.3.2 location store

```go
type Location struct {
    IATA   string  `json:"iata"`
    Lat    float64 `json:"lat"`
    Lon    float64 `json:"lon"`
    CCA2   string  `json:"cca2"`
    Region string  `json:"region"`
    City   string  `json:"city"`
}

type LocationStore interface {
    All() []Location
}
```

default impl: `FileLocationStore`:

- loads `[]Location` from `Config.LocationsFile` at startup
- panics or logs fatal if file can’t be read or parsed (fail fast)
- keeps locations in memory for fast serving

---

2.4 server struct
-----------------

```go
type Server struct {
    cfg          Config
    httpServer   *http.Server
    metaProvider MetaProvider
    locations    LocationStore
}
```

### initialization

- load defaults, overlay strictly parsed environment values, then explicit flags; no YAML/TOML loader is implemented
- build `metaProvider` based on geo configuration
- build `locations` from `LocationsFile`
- initialize the bounded measurement stream buffer pool and per-request payload generator
- configure `http.ServeMux` and route handlers
- create `http.Server` with `ReadHeaderTimeout` and `IdleTimeout`; apply control/transfer deadlines in endpoint wrappers
- optionally wrap `mux` in logging / recover / cors middleware

---

2.5 handlers
------------

### 2.5.1 helper: client ip extraction

Client identity is resolved by `internal/clientaddr.Resolver`. With no trusted
CIDRs it returns the direct TCP peer and ignores every forwarding header. With a
trusted direct peer it walks the complete `X-Forwarded-For` chain from right to
left, stopping at the first untrusted address. Empty, malformed, or partially
malformed chains are rejected rather than partially trusted.

The same resolved identity is used for metadata, logs, transfer/session limits,
byte quotas, rate limits, and packet-report ownership. This invariant prevents a
client from selecting a cheaper identity for one control surface than another.

---

### 2.5.2 `/meta`

handler flow:

1. build `ClientMeta` via `s.metaProvider.MetaFor(r)`;
2. set `maxTransferBytes`, `maxConcurrentTransfersPerClient`, and the three
   protocol capability versions from the daemon's implemented contract;
3. set `Content-Type: application/json; charset=utf-8`;
4. set `Cache-Control: no-store`;
5. JSON encode to `w`.

edge cases: none; errors should be extremely rare.

---

### 2.5.3 `/__down`

1. normalize the transport query through `measurementhttp.ParseDownload`;
2. enforce transfer admission and byte quota before committing a body;
3. apply anti-cache, `no-transform`, proxy-buffer suppression, metadata, and
   discriminator headers;
4. for streamed framing, commit a flush before writing so net/http cannot infer
   a small `Content-Length`;
5. stream exactly the requested bytes through `measurementhttp.Stream`, using
   per-request pseudorandom generation or a cleared zero buffer;
6. log selected payload, framing, application chunk size, bytes, duration, and
   interruption state.

The independent standard-library package `internal/measurementhttp` owns query
normalization and stream generation, allowing its exact-byte behavior to be
qualified without WebRTC or GeoIP dependencies.

---

### 2.5.4 `/__up`

1. reject non-identity content coding and a known `Content-Length` above
   `MaxBytes` before consuming it;
2. parse optional `bytes=N` and verify it against known and observed lengths;
3. read at most `MaxBytes + 1` so chunked or unknown-length overflow is visible;
4. reject read errors and declared-length mismatches;
5. on success, calculate body-ingestion duration and emit the verified receipt
   plus accepted-byte and framing diagnostics;
6. log the exact accepted bytes, correlation id, client address, duration, and
   calculated rate.

The implementation is `protocol.ReadUpload`; a plain `io.LimitReader(...,
MaxBytes)` is insufficient because reaching its artificial EOF does not reveal
that more request data existed.

### 2.5.5 `/__ping`

Accept `GET` and `HEAD`, apply the same admission and response controls as a
zero-byte download, and return `Content-Length: 0` with no body. The control
endpoint timeout applies and the connection remains reusable.

### 2.5.6 `/__ws`

1. require `GET`, `Upgrade: websocket`, version 13, and the exact
   `netspeed.ping.v1` subprotocol;
2. acquire one global and per-client transfer slot before switching protocols;
3. emit the RFC 6455 accept value, selected subprotocol, anti-transform,
   latency-measurement, and proxy-buffer-suppression headers;
4. hijack through the middleware response writer and keep the transfer read and
   write deadline on the connection;
5. validate each masked, unfragmented 16-byte `NSP1` application message and
   echo it unchanged, while handling ping, pong, and close control frames;
6. release the transfer slot on close, timeout, or protocol error.

### 2.5.7 `/locations`

handler flow:

1. `locs := s.locations.All()`
2. `Content-Type: application/json; charset=utf-8`
3. `Cache-Control: public, max-age=86400`
4. json encode `locs`

---

### 2.5.8 optional `/cdn-cgi/trace`

handler flow:

1. gather data:
   - client ip
   - http protocol
   - tls version (if tls)
   - colo (from meta)
   - loc (country code)
2. `Content-Type: text/plain; charset=utf-8`
3. write crude `key=value` pairs

---

2.6 cors
--------

if `EnableCORS` is `true`:

- handle `OPTIONS` requests for `/meta`, `/__down`, `/__up`, `/__ping`, `/locations`:

  - status: `204 No Content`
  - headers:

    ```http
    Access-Control-Allow-Origin: <origin or *>
    Timing-Allow-Origin: <origin or *>
    Access-Control-Allow-Methods: GET, HEAD, POST, OPTIONS
    Access-Control-Allow-Headers: Accept, Authorization, Cache-Control, Content-Encoding, Content-Type, Pragma, X-Requested-With
    Access-Control-Max-Age: 86400
    ```

- for actual `GET`, `HEAD`, and `POST` responses, add:

  ```http
  Access-Control-Allow-Origin: <origin or *>
  Timing-Allow-Origin: <origin or *>
  Access-Control-Expose-Headers: Content-Encoding, Content-Length, Server-Timing, X-Accel-Buffering, X-Netspeed-*
  ```

The implemented CORS middleware validates origins at startup and at request
time. Allowed origins receive `Access-Control-Allow-Origin` and matching
`Timing-Allow-Origin`; credentialed CORS requires explicit origins. The actual
exposure list names each measurement, receipt, quota, timing, and metadata
header rather than using the illustrative `X-Netspeed-*` shorthand above. Both
preflight and actual requests with a disallowed `Origin` receive `403` so an
unapproved page cannot consume measurement bandwidth blindly. Requests without
an `Origin` header are unaffected.

WebSocket handshakes do not use preflight, but browser handshakes carry
`Origin`. The same origin decision rejects a disallowed `/__ws` request before
the upgrade and transfer admission. If Fetch CORS is disabled, the WebSocket
handler must still reject a browser `Origin` whose host differs from the request
host; native clients without `Origin` remain valid. The response-writer wrapper
must preserve `http.Hijacker` for an allowed upgrade.

---

3. performance & robustness
===========================

3.1 concurrency & timeouts
--------------------------

- set `http.Server.ReadHeaderTimeout` to 10 seconds and `IdleTimeout` to two minutes by default;
- leave whole-request `ReadTimeout` and `WriteTimeout` disabled;
- apply a 30-second deadline to control/static routes, including `/__ping`, and
  a five-minute deadline to each upload, download, or persistent `/__ws`
  session;
- set only the read/write sides an endpoint needs, attach the same request-context deadline, and clear connection deadlines before keep-alive reuse;
- when a reverse proxy terminates TLS, disable buffering/compression/transformation on measurement routes and make proxy body/time limits at least as large as the daemon limits.

3.2 memory usage
----------------

- avoid allocating `bytes` bytes per request
- reuse bounded buffers from a pool; never allocate the requested transfer size
- goroutine per connection is fine; go’s runtime handles this well

3.3 safety limits
-----------------

- `MaxBytes` must be enforced for both `/__down` and `/__up`; `/__ws` uses a
  fixed 16-byte application payload and a bounded frame parser
- consider tighter defaults (e.g. 256 MiB) and allow override via config
- implement server-level rate limiting (ip-based, token bucket) if you plan to expose it publicly

3.4 payload generation
----------------------

The daemon pools buffers up to 1 MiB. `payload=random` refills each application
chunk from a per-request SplitMix64 stream so adjacent chunks do not repeat.
`payload=zero` clears the pooled buffer once and reuses it, minimizing generator
CPU. The pseudorandom stream is intended to resist transparent compression, not
to provide cryptographic bytes.

---

4. wiring with front-ends
=========================

4.1 cloudflare's `@cloudflare/speedtest` client
----------------------------------------------

example configuration in a browser:

```js
import SpeedTest from '@cloudflare/speedtest';

const st = new SpeedTest({
  downloadApiUrl: 'https://your-speed-host/__down',
  uploadApiUrl:   'https://your-speed-host/__up',
});

st.once('done', (summary) => {
  console.log(summary);
});
```

the go backend, as designed above, fully satisfies the expectations of this library:

- `__down` respects `bytes` and streams payload
- `__up` accepts uploads and finishes quickly
- `/__ping` provides warm zero-body HTTP latency while `/__down?bytes=0` remains compatible
- `/__ws` is a private Netspeed extension, not part of the Cloudflare client
  surface; strict clients use it only after exact capability advertisement
- optional `Server-Timing` improves latency diagnostics

4.2 your own spa
----------------

we build our own front-end (react/vue/svelte):

- use `/meta` for displaying:
  - isp (via as org)
  - city / region / country
  - colo (approx server location)
- use `/locations` to draw map/list of locations
- run parallel `/__down` + `/__up` tests with different `bytes` values to estimate throughput

---

5. minimal directory layout
===========================

example project tree:

```text
netspeed/
  cmd/
    netspeedd/
      main.go
  internal/
    server/
      server.go      // Server struct + handlers
      handlers.go    // individual handlers
    meta/
      provider.go    // MetaProvider interface + implementations
    locations/
      store.go       // LocationStore + file-based impl
  configs/
    netspeedd.env.example
    locations.example.json
  go.mod
  go.sum
  README.md
```

---

6. summary
==========

this spec defines a go-based speedtest backend that emulates the observable api surface used by speed.cloudflare.com:

- `/meta` - per-client metadata (ip, geo, asn, colo)
- `/__down` - exact download endpoint with payload, framing, chunk, and flush controls
- `/__up` - identity-only bounded upload endpoint with exact-byte receipt and read-duration proof
- `/__ws` - optional persistent 16-byte `netspeed.ping.v1` latency echo
- `/__ping` - zero-body warm-connection HTTP latency endpoint
- `/locations` - static list of test locations / colos
- optional `/cdn-cgi/trace` for debugging

the daemon is designed for high concurrency, streaming i/o, and pluggable metadata / location sources, and it's compatible with cloudflare's own `@cloudflare/speedtest` javascript client or your own spa.

---

# speedtest backend + TURN + UI spec

this doc extends the previous `cf-speed-daemon` http backend with:

- a TURN-based packet loss test, and
- a single-page web UI similar to `speed.cloudflare.com`.

it assumes the existing http api:

- `GET /meta`
- `GET /__down`
- `POST /__up`
- `GET /__ws` (advertised Netspeed WebSocket echo)
- `GET` or `HEAD /__ping`
- `GET /locations`

and adds:

- TURN credentials api
- WebRTC packet-loss api
- browser-side UI behavior & layout

---

## 1. TURN + WebRTC packet loss service

### 1.1 components

**infra pieces:**

- **ICE/TURN server** - three supported deployment modes:
  1. **STUN-only configuration**
     - one or more `stun:`/`stuns:` URLs;
     - no shared secret or username/credential is required.
  2. **embedded TURN server** (opt-in)
     - disabled by default and listens on `127.0.0.1:3478` when enabled;
     - a non-loopback listener requires an explicit advertised relay IP;
     - credentials are short-lived and the UDP socket has a combined byte-rate ceiling;
     - a random process-lifetime secret is generated when none is supplied.
  3. **external TURN server** (e.g. coturn)
     - example addresses:
       - `turn1.example.com:3478` (udp/tcp)
       - `turns1.example.com:5349` (tls)
     - config:
       - `realm = "speed.example.com"`
       - `use-auth-secret = yes`
       - `static-auth-secret = <shared-hmac-secret>`
- **go backend extensions (same daemon or sidecar):**
  - `GET /api/turn/credentials` → mints short-lived turn creds
  - `POST /api/packet-test/offer` → webRTC sdp offer/answer
  - `POST /api/packet-test/report` → returns authoritative directional counters

the browser never talks directly to the turn secret; it only sees derived username/password.

---

### 1.2 endpoint: `GET /api/turn/credentials`

**goal:** give the browser temporary TURN credentials & ice server list.

#### request

- method: `GET`
- path: `/api/turn/credentials`
- auth:
  - when `NETSPEEDD_ACCESS_TOKEN` is configured, require
    `Authorization: Bearer <token>`;
  - the endpoint also has a per-resolved-client token bucket;
- query params:
  - `ttl` (optional, int seconds) – requested lifetime, capped by server

#### response (200)

```jsonc
{
  "username": "1701532800:abcd1234",     // expiryTs:token
  "credential": "base64-hmac-here",     // hmac-sha1(secret, username)
  "ttlSec": 600,
  "servers": [
    "stun:turn1.example.com:3478",
    "turn:turn1.example.com:3478?transport=udp",
    "turn:turn1.example.com:3478?transport=tcp",
    "turns:turns1.example.com:5349?transport=tcp"
  ],
  "realm": "speed.example.com"
}
```

#### server behavior

1. compute expiry:

   ```go
   now := time.Now().Unix()
   ttl := clamp(requestedTTL, 60, cfg.MaxTurnTTL) // e.g. 600
   exp := now + ttl
   ```

2. generate a cryptographically random 16-byte token suffix for every TURN
   username;

3. build username:

   ```text
   username = "<exp>:<token>"
   ```

4. compute credential:

   ```go
   mac := hmac.New(sha1.New, []byte(cfg.TurnSecret))
   mac.Write([]byte(username))
   password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
   ```

5. respond with username, credential, ttlSec, servers, realm.

#### browser usage

```ts
const res = await fetch('/api/turn/credentials', { headers: serviceHeaders() });
const turn = await res.json();

const pc = new RTCPeerConnection({
  iceServers: [{
    urls: turn.servers,
    username: turn.username,
    credential: turn.credential
  }],
  iceTransportPolicy: 'relay' // force turn-only if you want
});
```

---

### 1.3 endpoint: `POST /api/packet-test/offer`

**goal:** perform webRTC signaling with a server-side peer for packet loss testing.

#### request

- method: `POST`
- path: `/api/packet-test/offer`
- headers: `Content-Type: application/json`
- body:

```jsonc
{
  "sdp": "<browser-offer-sdp>",
  "type": "offer",
  "testProfile": "loss-exact-v1"
}
```

#### response (200)

```jsonc
{
  "sdp": "<server-answer-sdp>",
  "type": "answer",
  "testId": "c65b0b1d-6f7f-4a9a-9f2b-7c9d3c5f0c3a"
}
```

`testId` is an opaque id you can use in logs/metrics.

#### server behavior (using e.g. pion/webrtc)

1. authenticate, rate-limit, bound the JSON body, and validate `type == "offer"`.
2. reserve global and per-client session capacity before allocating a peer connection.
3. create `PeerConnection` with the applicable ICE configuration.
4. register the session before callbacks, then set the remote description.
5. create and install the local answer under the cancellable lifecycle contract.
6. respond with answer SDP plus generated `testId`.
7. bind report completion to the same resolved client identity and close through
   the common manager-owned teardown path.

---

### 1.4 data channel packet loss protocol

The client creates an unordered data channel with `maxRetransmits: 0`. Every
message is an exact 1,200-byte binary frame; a short JSON object containing a
`size` field is not compliant.

#### 1.4.1 frame validation

The 32-byte big-endian header contains magic `NSPL`, frame version 1, probe/ack
type, header size, sequence, client send timestamp, daemon receive timestamp,
and declared frame size. The remaining 1,168 bytes use deterministic
sequence-derived padding. The daemon rejects wrong length, magic, version,
header, declared length, type, or padding.

The daemon tracks unique valid forward sequences, duplicates, invalid frames,
acknowledgements successfully submitted, and acknowledgement-send failures. It
acknowledges each unique valid probe once; duplicates do not increase the reverse
acknowledgement denominator.

#### 1.4.2 test and report

The supported clients send 1,000 probes at 10 ms intervals, wait three seconds
for late acknowledgements, and then call `POST /api/packet-test/report` with the
`testId` and transaction/RTT summary. The daemon snapshots the active session
before closing it and returns:

```json
{
  "ok": true,
  "protocolVersion": 2,
  "frameSizeBytes": 1200,
  "forwardReceived": 998,
  "acknowledgementsSent": 998,
  "duplicateFrames": 0,
  "invalidFrames": 0,
  "ackSendFailures": 0
}
```

These counters let clients calculate separately transaction loss, forward probe
loss, and reverse acknowledgement loss. The complete frame layout and formulas
are in `MEASUREMENT_PROTOCOL_V2.md`; manager ownership, disconnect grace, and
race-safe teardown are defined in `WEBRTC_LIFECYCLE.md`.

## 2. measurement pipeline

The daemon exposes bounded primitives; the Go and browser clients own the
window orchestration.

### 2.1 capabilities and exact transfers

`GET /meta` advertises protocol 2, upload receipt 1, packet frame 1, the
largest individual request, and optional measurement transport-controls version
1, including the exact WebSocket echo contract when enabled.
`/__down` streams exactly the requested bytes under the selected payload and
framing. `/__up` accepts exactly the declared/generated bytes under identity
content coding or returns an error and, on success, emits the verified receipt.

### 2.2 bounded fixed-duration windows

Each direction begins with three 100 kB and three 1 MB verified baselines. The
client then runs three 1.5-second windows. Request chunks remain between 100,000
bytes and 256 MiB, further capped by the daemon. Fast links scale with concurrent
repeated requests rather than a single giant transfer.

The daemon does not need a special window endpoint: every constituent request is
independently bounded and verifiable. Only completed verified requests count in
the client's aggregate window bytes.

### 2.3 continuous loaded latency

Clients prefer one persistent `/__ws` binary echo only when its exact path,
subprotocol, and 16-byte payload are advertised. One application warmup is not
reported, and RTT covers only send to matching nonce echo. Any upgrade or
message failure permanently selects zero-byte `/__ping`, then
`/__down?bytes=0` when needed, while one selected throughput window is active.
The browser caps load at five workers to reserve one conventional HTTP/1.1
origin connection for fallback. A probe is
retained only if at least one transfer body remains active for
the entire probe. Upload receipt wait does not count as outbound load. The daemon
accepts opaque `during`/`measId` labels for logging but does not claim overlap on
the client's behalf.

### 2.4 shared summaries

Supported clients use the R-7 p90 of valid fixed-window throughput values;
median latency after warmup removal and conservative IQR filtering; R-7
percentiles; p90-minus-median jitter; and population coefficient of variation.
Missing packet loss remains null and grades that require it are incomplete.

## 3. frontend data model

The detailed browser model lives in `netspeed-ui-spec.md`. The current fields
that the daemon must preserve on the wire are:

```ts
type ThroughputSample = {
  direction: 'download' | 'upload';
  sizeBytes: number;
  durationMs: number;
  mbps: number;
  profile: string;
  sampleKind?: 'baseline' | 'window';
  concurrency?: number;
  chunkBytes?: number;
  requestCount?: number;
};

type PacketLossResult = {
  sent: number;
  received: number;
  transactionLossPercent: number | null;
  forwardSent: number;
  forwardReceived: number;
  forwardLossPercent: number | null;
  acknowledgementsSent: number;
  acknowledgementsReceived: number;
  reverseAcknowledgementLossPercent: number | null;
  frameSizeBytes: 1200;
  unavailable?: boolean;
  reason?: string;
};
```

Unknown packet loss is null, never zero. The packet report response is the
source of the forward and acknowledgement-sent counters.

## 4. ui layout spec

single-page app with these sections, top to bottom.

### 4.1 top bar

- left: wordmark “Speed Test”.
- right:
  - text: “Built with <your platform>”
  - link to docs or marketing page.

sticky at top on scroll (optional).

---

### 4.2 hero metrics

three main columns on desktop, stacked on mobile.

**left column – download:**

- big number: `XXX` (download mbps).
- label: “Download”.
- small unit text: “Mbps”.
- info icon with tooltip.
- mini sparkline / area chart of download mbps over the test run.

**middle column – upload:**

- same as download but using upload mbps.

**right column – latency/jitter/loss:**

- main number: `latencyUnloadedMs` rounded (e.g. `6.0 ms`).
- subtext: `X–Y ms` range or percentile.
- secondary line with bullets:
  - `Jitter: X.X ms`
  - `Packet Loss: Y%`
- small timestamp “Measured at 11:52:04 AM”.

under columns, horizontal row:

- `[ Pause ]` button
- `[ Retest ]` button
- icons: share on twitter/x, copy link, download results (json).

---

### 4.3 network quality score

section title: “Network Quality Score” + “Learn more” link.

three equal columns:

- **Video Streaming:** colored dot + text grade (e.g. “Great”).
- **Online Gaming:** grade.
- **Video Chatting:** grade.

each uses `NetworkQuality` computed from `Summary` (section 2.3).

clicking “Learn more” opens a modal listing threshold table.

---

### 4.4 server location & latency

content row with two main columns.

#### left: server location card

- map widget (leaflet / mapbox / etc.).
  - marker for server location (from `/meta.colo` + `/locations`).
  - optional marker for approximate client location (from `meta.latitude/longitude`).
  - orange line connecting client → server.

- text list below:

  - `Connected via: IPv4` or `IPv6`.
  - `Server location: <city>`.
  - `Your network: <asOrganization> (AS<asn>)`.
  - `Your IP address: <clientIp>`.

#### right: latency measurements

stack of three accordion cards.

**1) “Unloaded latency (20/20)”**

- collapsed header: title + min/median/max summary.
- expanded:
  - horizontal graph:
    - x-axis 0–800 ms.
    - each sample as a small vertical bar/dot.
  - textual stats.

**2) “Latency during download (5)”**

- expanded card:
  - graph as above.
  - table:

    | # | Ping |
    |---|------|
    | 1 | 6 ms |
    | 2 | 41 ms |
    | - | -    |

**3) “Latency during upload (N)”**

- identical layout to “during download”.

---

### 4.5 packet loss measurements

single card.

- header: `Packet Loss Test (1000/1000)` (format: `received/expected`).
- big horizontal progress bar:
  - green segment length = `received / expected`.
  - grey remainder = lost.
- text below:

  - `Packet Loss: 0%`
  - `Received: 1000 / 1000 packets`
  - `Method: TURN/WebRTC DataChannel`

expanded view shows:

- RTT stats (min/median/p90).
- jitter estimate.
- turn server and protocol (e.g. `turn1.example.com:3478 (udp)`).

---

### 4.6 download measurements

grid of cards, 2 columns on desktop.

show the two planning baselines and three aggregate fixed windows:

- `100kB download baseline (3/3)`
- `1MB download baseline (3/3)`
- `download window 1`
- `download window 2 (loaded latency)`
- `download window 3`

**collapsed:**

- title as above.
- sparkline/histogram of mbps vs run.
- status bar: green (complete), yellow (running), grey (queued).

**expanded:**

- table:

  | # | Duration | Speed      |
  |---|----------|-----------|
  | 1 | 260 ms   | 769.24 Mb/s |
  | 2 | 464 ms   | 431.04 Mb/s |
  | 3 | …        | …         |

---

### 4.7 upload measurements

parallel grid for upload baselines and fixed windows:

- `100kB upload baseline (3/3)`
- `1MB upload baseline (3/3)`
- three upload windows, with the middle window owning loaded latency

show aggregate bytes, duration, flow count, request count, and Mbps for each
window. Same collapsed/expanded behavior as download.

---

### 4.8 footer

simple row:

- links:
  - `Home`
  - `About`
  - `Privacy Policy`
  - `Terms of Use`
- right-aligned logo (your brand).

---

## 5. frontend flow

1. **idle** — fetch metadata/locations and require protocol 2.
2. **running** — unloaded latency; verified download baselines and three fixed
   windows; verified upload baselines and three fixed windows; exact-frame packet
   test; stream progress into the UI. Loaded probes are owned by the middle
   throughput windows, not run afterward.
3. **complete** — calculate shared summary, grades, diagnostics, and the five
   confidence gates; expose JSON/share actions.
4. **error/partial** — transfer-contract failures stop the affected required
   operation; packet-test failure remains explicitly unavailable and does not become
   zero loss.

## 6. backend summary (net new endpoints)

relative to the original http-only daemon, this spec adds:

- `GET /api/turn/credentials`
  - returns temporary TURN username/password + ice servers.
- `POST /api/packet-test/offer`
  - webRTC signaling: browser offer → server answer.
- `POST /api/packet-test/report`
  - snapshots authoritative forward/ack counters, returns them, then closes the
    session.

`/meta` now advertises protocol and transport capabilities, `/__ws` supplies the
optional persistent latency echo, and `/__up` returns a verified exact-byte
receipt. These are wire-contract changes, not optional storage features.
