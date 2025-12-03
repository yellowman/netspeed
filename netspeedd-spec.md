netspeedd go-based speedtest backend
====================================

this document describes a go daemon that emulates the public api surface used by speed.cloudflare.com (based on cloudflare's published speedtest tooling and known public endpoints).

it covers:

- the endpoints:
  - `GET /meta`
  - `GET /__down`
  - `POST /__up`
  - `GET /locations`
  - (optional) `GET /cdn-cgi/trace`
- request/response semantics
- configuration, interfaces, and internal structure for a production-ready go server

---

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
  "timezone": "America/New_York"
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

**purpose**

provides binary payloads for:

- raw download throughput tests
- ttfb / latency tests (with `bytes=0` or very small)
- “latency under load” tests

**request**

- method: `GET`
- path: `/__down`
- query parameters (all optional, must accept extras):

  - `bytes` (string -> int64)
    - meaning: number of bytes to return in the response body
    - default: 0 (latency-only test)
    - min: `0`
    - max: configurable (`Config.MaxBytes`, default e.g. 1 GiB)
  - `measId` (string)
    - client-side measurement id for correlation
    - server treats as opaque; logs it if desired
  - `during` (string)
    - semantic labels like `download`, `upload`, etc.
    - safe for server to ignore

- body: none

**response**

- status:
  - `200 OK` on success
  - `400 Bad Request` if `bytes` is invalid or exceeds configured max (or you can clamp it and still return `200`)
- headers:
  - `Content-Type: application/octet-stream`
  - `Content-Length: <bytes>`
  - optional metadata headers (cf-style):

    ```http
    cf-meta-asn: <int>
    cf-meta-city: <string>
    cf-meta-colo: <iata>
    cf-meta-country: <cca2>
    cf-meta-ip: <ip>
    cf-meta-latitude: <float>
    cf-meta-longitude: <float>
    cf-meta-postalcode: <string>
    cf-meta-request-time: <unix_ms>
    cf-meta-timezone: <tz>
    ```

  - optional server timing header:

    ```http
    server-timing: app;dur=<ms>
    ```

- body:
  - exactly `bytes` bytes of arbitrary data
  - content can be random or deterministic; clients only care about size and timing

**behavior**

1. parse `bytes` from query string:
   - if empty - 0
   - if non-integer - respond `400`
   - if negative - respond `400`
   - if > `Config.MaxBytes`:
     - either respond `400`, or
     - clamp to `Config.MaxBytes` (document whichever you choose)
2. record `start := time.Now()` if `ServerTiming` enabled
3. write headers (including `Content-Length`)
4. if `bytes == 0`, write no body and return
5. otherwise, stream `bytes` bytes using a reusable buffer (see section 3.4)
6. if `ServerTiming` enabled, compute `durMs := time.Since(start) / time.Millisecond` and set `Server-Timing: app;dur=<durMs>`

---

### 1.1.3 `POST /__up` - upload sink

**purpose**

accepts large request bodies so the client can measure upload throughput and latency under load.

**request**

- method: `POST`
- path: `/__up`
- query parameters:
  - `measId` (optional, as with `/__down`)
- headers:
  - `Content-Type`: typically `application/octet-stream` (but server SHOULD ignore the type)
  - `Content-Length` or `Transfer-Encoding: chunked`
- body:
  - arbitrary bytes
  - potentially very large; must be bounded by configuration

**response**

- status: `200 OK` on success
- headers:
  - `Content-Type: application/json` or `text/plain` (client doesn’t care)
  - optional `Server-Timing: app;dur=<ms>`
- body:
  - may be empty or trivial (`{"ok":true}`); client uses only timing

**behavior**

1. record `start := time.Now()` if `ServerTiming` enabled
2. read and discard body safely with a limit:

   ```go
   n, err := io.Copy(io.Discard, io.LimitReader(r.Body, cfg.MaxBytes))
   ```

3. log `n`, `measId`, client ip, and duration
4. write `200` with small body or no body
5. if `ServerTiming` enabled, add `Server-Timing` as above

the important point: **always** read the request body fully up to a safe limit to avoid leaving connections hanging.

---

### 1.1.4 `GET /locations` - colo list

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

- serve `/meta`, `/__down`, `/__up`, `/locations` (and optional `/cdn-cgi/trace`)
- expose simple configuration (listen address, tls, limits, cors, geo db, locations file)
- be robust under very high concurrency and long-lived connections

2.2 configuration model
-----------------------

```go
type Config struct {
    ListenAddr          string        // ":8080" or ":443"
    TLSCertFile         string        // if empty => http only
    TLSKeyFile          string

    MaxBytes            int64         // hard cap for bytes/upload, e.g. 1<<30
    DefaultMaxBytes     int64         // optional lower per-request limit

    ReadTimeout         time.Duration
    WriteTimeout        time.Duration
    IdleTimeout         time.Duration

    EnableServerTiming  bool

    EnableCORS          bool
    AllowedOrigins      []string      // or "*" for public

    LocationsFile       string        // path to JSON file with Location list

    // meta / geo configuration
    GeoIPDatabasePath   string        // optional maxmind db path
    TrustProxyHeaders   bool          // whether to honor X-Forwarded-For, etc.
}
```

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
    Timezone      string  `json:"timezone,omitempty"`
}

type MetaProvider interface {
    MetaFor(r *http.Request) ClientMeta
}
```

**implementations**

1. `StaticMetaProvider`  
   - returns fixed values (handy for testing).

2. `GeoIPMetaProvider`  
   - uses `GeoIPDatabasePath` and client ip to look up:
     - country, region, city, postal code
     - latitude, longitude
     - asn and as org
   - maps country / region / city to closest colo or uses a separate colo map.

3. `HeaderMetaProvider`  
   - reads “trusted” upstream headers like `CF-IPCountry`, `CF-Connecting-IP`, `CF-Region`, etc.
   - useful when your daemon sits behind an existing cdn and wants to reuse their metadata.

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

    payloadBuf   []byte  // shared download buffer
}
```

### initialization

- parse config (flags/env/toml/yaml — up to you)
- build `metaProvider` based on geo configuration
- build `locations` from `LocationsFile`
- allocate `payloadBuf` (e.g. 1 MiB or 4 MiB), optionally fill with random bytes
- configure `http.ServeMux` and route handlers
- create `http.Server` with timeouts from config
- optionally wrap `mux` in logging / recover / cors middleware

---

2.5 handlers
------------

### 2.5.1 helper: client ip extraction

```go
func clientIPFromRequest(r *http.Request, trustProxy bool) string {
    if trustProxy {
        if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
            // take the first entry before the first comma
            if idx := strings.Index(xff, ","); idx != -1 {
                return strings.TrimSpace(xff[:idx])
            }
            return strings.TrimSpace(xff)
        }
        if cip := r.Header.Get("CF-Connecting-IP"); cip != "" {
            return cip
        }
    }
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil {
        return r.RemoteAddr // fallback; not ideal but ok
    }
    return host
}
```

---

### 2.5.2 `/meta`

handler flow:

1. build `ClientMeta` via `s.metaProvider.MetaFor(r)`
2. set `Content-Type: application/json; charset=utf-8`
3. set `Cache-Control: no-store`
4. json encode to `w`

edge cases: none; errors should be extremely rare.

---

### 2.5.3 `/__down`

handler flow:

1. parse `bytes`:

   ```go
   bytesStr := r.URL.Query().Get("bytes")
   var nBytes int64
   if bytesStr != "" {
       v, err := strconv.ParseInt(bytesStr, 10, 64)
       if err != nil || v < 0 {
           http.Error(w, "invalid bytes", http.StatusBadRequest)
           return
       }
       if v > s.cfg.MaxBytes {
           http.Error(w, "bytes too large", http.StatusBadRequest)
           return
       }
       nBytes = v
   }
   ```

2. record `start := time.Now()` if `EnableServerTiming`
3. set headers:
   - `Content-Type`
   - `Content-Length`
   - meta headers built from `MetaProvider` (optionally; or reuse same meta as `/meta`)
4. if `nBytes == 0`, just `w.WriteHeader(http.StatusOK)` and return
5. else, stream:

   ```go
   buf := s.payloadBuf
   remaining := nBytes
   for remaining > 0 {
       chunk := int64(len(buf))
       if remaining < chunk {
           chunk = remaining
       }
       if _, err := w.Write(buf[:chunk]); err != nil {
           // client probably disconnected; log and abort
           return
       }
       remaining -= chunk
   }
   ```

6. if `EnableServerTiming`, compute `durMs` and add `Server-Timing` header

---

### 2.5.4 `/__up`

handler flow:

1. record `start := time.Now()` if `EnableServerTiming`
2. read request body:

   ```go
   max := s.cfg.MaxBytes
   n, err := io.Copy(io.Discard, io.LimitReader(r.Body, max))
   if err != nil && !errors.Is(err, io.EOF) {
       // log error; we might still write a 500 or 200 depending on severity
   }
   ```

3. set `Content-Type: application/json; charset=utf-8`
4. if `EnableServerTiming`, set `Server-Timing`
5. write `{"ok":true}` or `204 No Content`

---

### 2.5.5 `/locations`

handler flow:

1. `locs := s.locations.All()`
2. `Content-Type: application/json; charset=utf-8`
3. `Cache-Control: public, max-age=86400`
4. json encode `locs`

---

### 2.5.6 optional `/cdn-cgi/trace`

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

- handle `OPTIONS` requests for `/meta`, `/__down`, `/__up`, `/locations`:

  - status: `204 No Content`
  - headers:

    ```http
    Access-Control-Allow-Origin: <origin or *>
    Access-Control-Allow-Methods: GET, POST, OPTIONS
    Access-Control-Allow-Headers: Content-Type, X-Requested-With
    Access-Control-Max-Age: 86400
    ```

- for actual `GET` / `POST` responses, add:

  ```http
  Access-Control-Allow-Origin: <origin or *>
  ```

if you want to strictly validate allowed origins, you can:

- check `Origin` header
- allow only if it’s in `AllowedOrigins`
- otherwise omit `Access-Control-Allow-Origin` (which blocks browser use)

---

3. performance & robustness
===========================

3.1 concurrency & timeouts
--------------------------

- use `http.Server` with:

  ```go
  ReadTimeout:  15 * time.Second,
  WriteTimeout: 60 * time.Second,  // uploads/downloads can be long
  IdleTimeout:  120 * time.Second,
  ```

- consider a reverse proxy (nginx/haProxy) in front for slow client protection and tls termination, but not required.

3.2 memory usage
----------------

- avoid allocating `bytes` bytes per request
- a single shared `payloadBuf` of 1–4 MiB and streaming loop is adequate
- goroutine per connection is fine; go’s runtime handles this well

3.3 safety limits
-----------------

- `MaxBytes` must be enforced for both `/__down` and `/__up`
- consider tighter defaults (e.g. 256 MiB) and allow override via config
- implement server-level rate limiting (ip-based, token bucket) if you plan to expose it publicly

3.4 payload buffer generation
-----------------------------

at startup:

```go
bufSize := 1 << 20 // 1 MiB
buf := make([]byte, bufSize)
if _, err := rand.Read(buf); err != nil {
    // fallback to zeros or a deterministic pattern
}
s.payloadBuf = buf
```

since content doesn’t need to be cryptographically strong, but `crypto/rand` is fine too. `math/rand` is usually enough here.

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
- optional: return `Server-Timing` to improve latency accuracy

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
    config.example.yaml
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
- `/__down` - download / latency endpoint with `bytes` control and optional cf-style meta headers and `Server-Timing`
- `/__up` - upload sink that safely reads and discards large bodies
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
- `GET /locations`

and adds:

- TURN credentials api
- WebRTC packet-loss api
- browser-side UI behavior & layout

---

## 1. TURN + WebRTC packet loss service

### 1.1 components

**infra pieces:**

- **turn server** (e.g. coturn)
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
  - optional `POST /api/packet-test/report` → client sends final stats for storage

the browser never talks directly to the turn secret; it only sees derived username/password.

---

### 1.2 endpoint: `GET /api/turn/credentials`

**goal:** give the browser temporary TURN credentials & ice server list.

#### request

- method: `GET`
- path: `/api/turn/credentials`
- auth:
  - at minimum, **same-origin** cookie/session
  - optionally require a short-lived app jwt
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

2. generate `token` (optional):

   - either random
   - or derived from user id / session id

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
const res = await fetch('/api/turn/credentials', { credentials: 'include' });
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
  "testProfile": "loss-basic"   // optional; see below
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

1. read request, validate `type == "offer"`.
2. create `PeerConnection` with same `iceServers` you gave the client.
3. set remote description to the offer sdp.
4. create answer, set local description.
5. register `OnDataChannel` handler for the `"packet-loss"` channel.
6. respond with answer sdp + generated `testId`.
7. keep the peer connection alive long enough for the test to run (e.g. 30s timeout).

---

### 1.4 data channel packet loss protocol

#### 1.4.1 channel setup

- browser:

  ```ts
  const dc = pc.createDataChannel('packet-loss', {
    ordered: true,
    maxRetransmits: 0
  });
  ```

- server:

  - in `OnDataChannel` callback, expect label `"packet-loss"`.
  - set `OnMessage` to handle incoming packets.
  - optionally reply with acks.

#### 1.4.2 packet format

use a small json message per packet; binary is also fine but json is easier to inspect:

```jsonc
{
  "seq": 123,                  // 0..N-1
  "sentAt": 1701532800123,     // ms since epoch (client clock)
  "size": 1200                 // intended payload size in bytes
}
```

ack from server:

```jsonc
{ "ack": 123 }
```

#### 1.4.3 test profile: `loss-basic`

**parameters:**

- total packets: `N = 1000`
- size per packet: ~1200 bytes of payload (close to mtu but under)
- send rate: 100 packets/sec (10 seconds)
- direction: browser → server; server responds with acks

**browser behavior:**

1. after data channel opens, start a timer loop:

   ```ts
   const N = 1000;
   const interval = 10; // ms, ~100 packets/sec
   let seq = 0;
   const acks = new Set<number>();

   dc.onmessage = (ev) => {
     const msg = JSON.parse(ev.data);
     if (typeof msg.ack === 'number') acks.add(msg.ack);
   };

   const timer = setInterval(() => {
     if (seq >= N) {
       clearInterval(timer);
       return;
     }

     const payloadSize = 1200;
     const msg = {
       seq,
       sentAt: Date.now(),
       size: payloadSize
     };
     dc.send(JSON.stringify(msg));
     seq++;
   }, interval);
   ```

2. after all packets are sent, wait `extraWaitMs` (e.g. 3000 ms) for late acks.
3. compute:

   ```ts
   const sent = N;
   const received = acks.size;
   const lossPercent = (sent - received) / sent * 100;
   ```

4. optionally collect webRTC stats via `pc.getStats()` for rtt/jitter.

5. optionally `POST /api/packet-test/report` with the final stats.

**server behavior:**

1. track:

   ```go
   totalRecv := 0
   lastSeq := -1
   ```

2. on each message:

   - parse json
   - increment `totalRecv`
   - write ack:

     ```go
     ack := map[string]int{"ack": seq}
     // encode as json and send back
     ```

3. on channel close or timeout, log `(testId, totalRecv)`.

---

## 2. measurement pipeline

the ui orchestrates four categories of tests:

1. **download speed** (http `/__down`)
2. **upload speed** (http `/__up`)
3. **latency** (small `/__down?bytes=0` probes)
4. **packet loss** (webrtc/turn)

### 2.1 test profiles

#### 2.1.1 download

example sizes (matching the screenshot):

- 100 kB
- 1 MB
- 10 MB
- 25 MB
- 100 MB

for each profile:

- run 3–10 iterations.
- record:
  - start/end timestamps
  - duration ms
  - exact bytes transferred
  - computed mbps.

http request shape:

```text
GET /__down?bytes=<sizeInBytes>&profile=<label>&run=<i>
```

#### 2.1.2 upload

sizes:

- 100 kB
- 1 MB
- 10 MB
- 25 MB
- 50 MB

for each profile:

- generate payload in js (arraybuffer or blob).
- `POST /__up?profile=<label>&run=<i>` with body size = profile size.
- same captured metrics.

#### 2.1.3 latency

- **unloaded:** 20 sequential probes

  ```text
  GET /__down?bytes=0&phase=unloaded&seq=N
  ```

- **during download:**
  - while a medium/large download test is in progress, send 5 probes:

    ```text
    GET /__down?bytes=0&phase=download&seq=N
    ```

- **during upload:** same idea with `phase=upload`.

record per-probe rtt from browser.

#### 2.1.4 packet loss

- run a single `loss-basic` webrtc test as defined in section 1.4.
- record:
  - `sent`
  - `received`
  - `lossPercent`
  - rtt stats & jitter from `getStats()`.

---

### 2.2 summary metrics

from all collected samples:

```ts
type Summary = {
  downloadMbps: number;        // e.g. 90th percentile of download mbps
  uploadMbps: number;          // 90th percentile of upload mbps
  latencyUnloadedMs: number;   // median unloaded latency
  latencyDownloadMs: number;   // 90th percentile during-download
  latencyUploadMs: number;     // 90th percentile during-upload
  jitterMs: number;            // jitter estimate (p90 - median or stddev)
  packetLossPercent: number;   // from webrtc
};
```

**example computations:**

- `downloadMbps`:
  - collect all download samples `mbps`.
  - sort and take 90th percentile.

- `jitterMs`:
  - from unloaded latency:
    - `jitter = p90(unloadedLatency) - median(unloadedLatency)`.

- `packetLossPercent`:
  - `lossPercent = (sent - received) / sent * 100`.

---

### 2.3 network quality grading

grades:

```text
Great, Good, Okay, Poor
```

thresholds (example, tweak to taste):

```ts
function gradeForStreaming(summary: Summary): NetworkQualityGrade {
  const { downloadMbps, latencyUnloadedMs, jitterMs, packetLossPercent } = summary;

  if (
    downloadMbps >= 50 &&
    latencyUnloadedMs <= 25 &&
    jitterMs <= 5 &&
    packetLossPercent <= 0.5
  ) return 'Great';

  if (
    downloadMbps >= 20 &&
    latencyUnloadedMs <= 50 &&
    jitterMs <= 15 &&
    packetLossPercent <= 1.5
  ) return 'Good';

  if (
    downloadMbps >= 10 &&
    latencyUnloadedMs <= 80 &&
    jitterMs <= 30 &&
    packetLossPercent <= 3
  ) return 'Okay';

  return 'Poor';
}
```

gaming / video chat can be stricter on latency/jitter, looser on throughput.

---

## 3. frontend data model

typescript types used by the spa:

```ts
type LatencySample = {
  ts: number;
  rttMs: number;
  phase: 'unloaded' | 'download' | 'upload';
};

type ThroughputSample = {
  ts: number;
  direction: 'download' | 'upload';
  sizeBytes: number;
  durationMs: number;
  mbps: number;
  profile: '100k' | '1M' | '10M' | '25M' | '50M' | '100M';
  runIndex: number;
};

type PacketLossResult = {
  sent: number;
  received: number;
  lossPercent: number;
  rttStatsMs: {
    min: number;
    median: number;
    p90: number;
  };
  jitterMs: number;
};

type Summary = {
  downloadMbps: number;
  uploadMbps: number;
  latencyUnloadedMs: number;
  latencyDownloadMs: number;
  latencyUploadMs: number;
  jitterMs: number;
  packetLossPercent: number;
};

type NetworkQualityGrade = 'Great' | 'Good' | 'Okay' | 'Poor';

type NetworkQuality = {
  videoStreaming: NetworkQualityGrade;
  gaming: NetworkQualityGrade;
  videoChatting: NetworkQualityGrade;
};

type Meta = {
  hostname: string;
  clientIp: string;
  httpProtocol: string;
  asn: number;
  asOrganization: string;
  colo: string;
  country: string;
  city: string;
  region: string;
  postalCode: string;
  latitude: number;
  longitude: number;
  timezone?: string;
};
```

---

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

for each profile:

- `100kB download test (10/10)`
- `1MB download test (8/8)`
- `10MB download test (6/6)`
- `25MB download test (4/4)`
- `100MB download test (3/3)`

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

parallel grid of cards for upload profiles:

- `100kB upload test`
- `1MB upload test`
- `10MB upload test`
- `25MB upload test`
- `50MB upload test`

same collapsed/expanded behavior as download.

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

high-level state machine:

1. **idle**
   - page loaded.
   - meta/location fetched.
   - ui shows “Start test” button.

2. **running**
   - disable “retest”, show “pause”.
   - orchestrate tests in phases:
     1. quick unloaded latency & small download/upload.
     2. full download profiles.
     3. full upload profiles.
     4. during-download / during-upload latency probes.
     5. webrtc packet loss test.
   - stream interim results into ui.

3. **complete**
   - compute `Summary` + `NetworkQuality`.
   - enable “retest”.
   - expose “download results” (json) and “share link” actions.

4. **error**
   - if any step fails (no turn, blocked http, etc.), show minimal error banner but still show partial results when possible.

---

## 6. backend summary (net new endpoints)

relative to the original http-only daemon, this spec adds:

- `GET /api/turn/credentials`
  - returns temporary TURN username/password + ice servers.
- `POST /api/packet-test/offer`
  - webRTC signaling: browser offer → server answer.
- optional: `POST /api/packet-test/report`
  - body: `PacketLossResult` + `Summary` for storing historical data.

everything else (meta, down, up, locations) is unchanged from the previous spec.
