# HTTP measurement transport discriminators

This document defines version 1 of Netspeed's optional HTTP transport-control
extension. It extends measurement protocol v2 without changing the verified
byte-count, receipt, window, or statistics rules in
[`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md).

The daemon implements this contract and advertises it in `/meta`. Clients that
do not understand `measurementCapabilities` remain compatible: the default
`/__down?bytes=N` and `/__up` behavior is unchanged. The daemon also advertises
an optional application-level WebSocket echo for low-overhead latency. Capable
clients prefer it only when the complete contract is present and permanently
fall back to the advertised warm HTTP probe after any upgrade or protocol
failure.

## 1. Capability advertisement

`GET /meta` includes a `measurementCapabilities` object similar to:

```json
{
  "version": 1,
  "downloadPath": "/__down",
  "downloadBytesParameter": "bytes",
  "downloadPayloadParameter": "payload",
  "downloadFramingParameter": "framing",
  "downloadChunkBytesParameter": "chunkBytes",
  "downloadFlushParameter": "flush",
  "uploadPath": "/__up",
  "uploadBytesParameter": "bytes",
  "httpPingPath": "/__ping",
  "httpPingMethods": ["GET", "HEAD"],
  "webSocketPingPath": "/__ws",
  "webSocketPingProtocol": "netspeed.ping.v1",
  "webSocketPingPayloadBytes": 16,
  "warmConnectionPing": true,
  "downloadPayloads": ["random", "zero"],
  "downloadFramings": ["fixed", "chunked"],
  "defaultDownloadPayload": "random",
  "defaultDownloadFraming": "fixed",
  "defaultChunkBytes": 1048576,
  "minimumChunkBytes": 4096,
  "maximumChunkBytes": 1048576,
  "uploadContentEncodings": ["identity"],
  "responseCacheControl": "no-store, no-transform",
  "noTransform": true,
  "proxyBufferSuppressionHeader": "X-Accel-Buffering: no",
  "proxyRequestBufferingAdvisory": true
}
```

Parameter names are advertised rather than assumed so Go, C, and browser clients
can converge on the same behavior. `proxyRequestBufferingAdvisory` means a
response header cannot disable buffering of an upload request body; the reverse
proxy must be configured accordingly.

### 1.1 Strict Netspeed client negotiation and safety

The Go CLI, native C CLI, and browser validate the complete object before
issuing a measurement request. Endpoint values must be clean, same-origin
relative paths with no authority, query, fragment, traversal, or backslash form.
Download query parameter names must be syntactically safe and distinct, so
hostile or malformed metadata cannot redirect traffic or collapse two
discriminators onto one key.

The corresponding CLI controls are:

```text
--download-payload auto|random|zero
--download-framing auto|fixed|chunked
--download-chunk-bytes N
--download-flush auto|true|false
```

`auto` uses the daemon's advertised defaults. `download-chunk-bytes=0` uses the
advertised default chunk size, and automatic flushing is enabled for streamed
framing and disabled for fixed framing. Explicit choices fail negotiation when
the daemon does not advertise transport version 1 or the selected value. They
are never silently sent to a legacy endpoint.

The browser exposes the same choices through configuration loaded before the
measurement engine:

```html
<script>
  globalThis.NETSPEED_CONFIG = {
    measurementTransport: {
      downloadPayload: "auto",       // auto | random | zero
      downloadFraming: "auto",       // auto | fixed | chunked
      downloadChunkBytes: 0,          // 0 uses the advertised default
      downloadFlush: "auto"           // auto | true | false
    }
  };
</script>
<script src="js/http_transport.js"></script>
<script src="js/speedtest.js"></script>
```

For every run, the normalized contract is included in result metadata as
`measurementSelection`. The browser also publishes it under
`httpTransport.selection`. It records the selected payload, framing,
application chunk size, flush behavior, upload encoding, HTTP latency path and
method, exact WebSocket path/subprotocol/payload size, preferred latency
transport, HTTP-fallback availability, warm-reuse requirement, and whether
legacy fallback was used. All three clients verify the response's measurement
type, payload, framing, chunk size, flush value, exact length, identity content
coding, and `no-store, no-transform` controls. The native C process fixture and
browser integration tests advertise nonstandard endpoint paths and query names,
so qualification proves that clients consume the advertisement rather than
falling back to hard-coded routes.

Browser scripts cannot set the forbidden `Accept-Encoding` request header. The
browser instead requests `no-store, no-transform`, sends
`Content-Encoding: identity` on uploads, requires `Content-Encoding` to be
absent or `identity` on measurement responses, and records this limitation in
`httpTransport.requestControls`. It samples bounded windows across download
bodies to verify zero-fill exactly and reject low-diversity data mislabeled as
pseudorandom without retaining the complete transfer.

### 1.2 Go and native C Cloudflare behavioral negotiation

Cloudflare compatibility cannot assume the daemon's capability object exists.
Both native adapters therefore use a bounded 64 KiB request to the common
`/__down?bytes=N` surface and record the observed provider defaults under
`httpTransport`:

- binary zero-fill, ASCII-zero, incompressible random, or opaque payload;
- exact-length fixed, HTTP/1.x chunked, or lengthless streamed framing;
- optional exact chunk and flush evidence from `X-Netspeed-Chunk-Bytes` and
  `X-Netspeed-Flush`.

The adapters identify this behavior as `cloudflare-http-v2`. `auto` accepts the
observed defaults. Explicit CLI values are requirements: they succeed only when
the probe proves the endpoint already behaves that way. No `payload`, `framing`,
`chunkBytes`, or `flush` query is sent to force an unadvertised choice. Every
later download is checked for drift from the probe using bounded evidence
windows distributed across the body.

All requests send `Accept-Encoding: identity`, `Cache-Control: no-store,
no-transform`, and `Pragma: no-cache`; uploads also send `Content-Encoding:
identity`. Automatic decompression is disabled and any non-identity response
coding is fatal. The probe reports, but cannot manufacture, remote response
controls such as `no-transform` or `X-Accel-Buffering: no`.

The browser's strict Netspeed path now uses the advertised transport contract.
A browser Cloudflare behavioral adapter is a separate compatibility concern and
is not inferred from a missing or malformed Netspeed advertisement.

## 2. Download endpoint

```text
GET /__down?bytes=N&payload=random|zero&framing=fixed|chunked&chunkBytes=N&flush=true|false
```

All parameters are optional. Unknown parameters remain accepted as opaque
correlation or profile labels.

| parameter | default | contract |
|---|---|---|
| `bytes` | `0` | Exact response-body size from zero through `maxTransferBytes`. |
| `payload` | `random` | `random` emits a nonrepeating per-request pseudorandom stream; `zero` emits zero-fill. Aliases `pattern` and `fill` are accepted. |
| `framing` | `fixed` | `fixed` supplies `Content-Length`; `chunked` deliberately omits it and commits streamed framing. Alias `stream` is accepted. |
| `chunkBytes` | `1048576` | Application write size, bounded from 4 KiB through 1 MiB. |
| `flush` | `true` for `chunked`, otherwise `false` | Flush after every application chunk. `chunked&flush=false` still commits streamed framing once, but avoids a flush syscall per chunk. |

`chunked` names logical streamed framing. HTTP/1.1 normally represents this with
chunked transfer coding; HTTP/2 and HTTP/3 represent it with DATA frames and no
`Content-Length`.

### 2.1 Payload selection

`payload=random` is the integrity-oriented default. The daemon fills each chunk
from a per-request SplitMix64 stream, so it does not replay one small random
buffer for the entire transfer. This makes repeated-pattern compression or WAN
optimization substantially less likely to inflate a throughput result. The
stream is measurement data, not cryptographic randomness.

`payload=zero` minimizes generation cost for CPU-constrained servers and very
high-rate links. It deliberately permits any compression or optimization in the
path to become visible as a different test condition. Clients must label the
selected payload rather than compare zero-fill and pseudorandom results as if
they were the same experiment.

### 2.2 Response verification

A successful fixed response has:

```http
Content-Type: application/octet-stream
Content-Length: <requested bytes>
X-Netspeed-Payload: random|zero
X-Netspeed-Framing: fixed
X-Netspeed-Chunk-Bytes: <application chunk bytes>
X-Netspeed-Flush: true|false
```

A successful streamed response has the same diagnostic headers, except
`X-Netspeed-Framing: chunked` and no `Content-Length`. In either mode, the client
must consume exactly the requested number of body bytes. Framing never weakens
exact-byte verification.

## 3. Upload endpoint

```text
POST /__up?bytes=N&measId=<opaque>
Content-Type: application/octet-stream
Content-Encoding: identity
```

The `bytes` discriminator is optional. When present, the daemon verifies it
against both a known `Content-Length` and the number of bytes actually consumed.
A mismatch is `400 Bad Request`. A declared or observed body beyond
`maxTransferBytes` is `413 Request Entity Too Large`.

The daemon accepts an absent `Content-Encoding` or `identity`. It rejects gzip,
Brotli, and every other content coding with `415 Unsupported Media Type`, because
measuring a decoded body would mix network throughput with proxy and
server-side decompression.

A successful response preserves the protocol-v2 receipt and adds diagnostics:

```http
X-Netspeed-Payload: discarded
X-Netspeed-Framing: fixed|chunked
X-Netspeed-Content-Encoding: identity
X-Netspeed-Expected-Bytes: <bytes parameter, when supplied>
X-Netspeed-Accepted-Bytes: <consumed bytes>
X-Netspeed-Upload-Duration-Ns: <body ingestion duration>
```

`serverDurationNs` and `X-Netspeed-Upload-Duration-Ns` span request-body
consumption only. Admission, handler setup, and JSON receipt generation are not
included.

## 4. Latency transports

### 4.1 Preferred application-level WebSocket echo

A daemon advertising all three WebSocket fields offers:

```text
GET /__ws
Sec-WebSocket-Protocol: netspeed.ping.v1
```

The route requires an HTTP/1.1 RFC 6455 upgrade and selects exactly the
`netspeed.ping.v1` subprotocol. Each application ping is one unfragmented binary
message with exactly 16 bytes:

```text
bytes 0..3   ASCII "NSP1"
bytes 4..7   unsigned 32-bit sequence number, network byte order
bytes 8..15  per-message random nonce
```

The daemon validates the size and magic and echoes the complete message
unchanged. It accepts masked client frames, sends unmasked server frames,
answers WebSocket control pings with pongs, and rejects text, fragmented,
oversized, or malformed application messages. The client compares the complete
nonce, so an old or duplicated echo cannot satisfy a later probe.

A successful upgrade selects the exact subprotocol and carries:

```http
HTTP/1.1 101 Switching Protocols
Cache-Control: no-store, no-transform
Pragma: no-cache
X-Accel-Buffering: no
X-Netspeed-Measurement: latency
```

The Go and native C clients validate every repeated or comma-separated
`Content-Encoding` value, all required upgrade fields, and all singleton
diagnostic fields. A leading `identity` cannot conceal a later `gzip` or `br`,
and conflicting repeated diagnostics are rejected rather than trusting the
first header line. Browser JavaScript cannot inspect 101 response headers; it
validates the selected subprotocol and every echoed binary nonce instead.

The Go, native C, and browser Netspeed clients:

1. use WebSocket only when `/meta` advertises the exact path, subprotocol, and
   16-byte payload contract;
2. establish one persistent connection and send one unreported application
   warmup;
3. start the RTT clock immediately before sending a measured binary message and
   stop it when the exact nonce returns, excluding DNS, TCP, TLS, HTTP Upgrade,
   and warmup time;
4. label accepted samples with `probeTransport: "websocket"`,
   `probeMethod: "MESSAGE"`, `timingSource: "websocket-message"`, the advertised
   path, and `webSocketProtocol: "netspeed.ping.v1"`;
5. disable WebSocket for the remainder of the run after the first upgrade,
   close, timeout, framing, subprotocol, or echo failure, then use the existing
   warm HTTP path for every later probe. HTTP samples retain the stable reason in
   `probeFallbackReason`.

Firefox and privacy-hardened browsers may quantize `performance.now()` enough
that a very fast echo begins and ends in the same visible timer tick. Such a
message is valid rather than a transport failure. The browser preserves it as
`rawRttMs: 0`, reports the positive statistics-compatible representation floor
`rttMs: 0.01`, and sets `timingResolutionLimited: true` plus
`timerRepresentationFloorMs: 0.01`. The transport evidence counts these as
`timingResolutionLimitedMessages`. A negative or non-finite duration still
disables WebSocket and falls back to HTTP. The browser keeps the matching
request pending until payload and timing validation finish, ensuring any
failure rejects the caller instead of stranding the test in the latency stage.

This is an application message echo rather than an RFC 6455 control ping because
the browser WebSocket API cannot originate control frames. The browser also
cannot attach an `Authorization` header or exactly reproduce Fetch credential
suppression. It therefore selects HTTP immediately when a bearer token is
configured, when credentials are `omit`, or when `same-origin` credentials are
combined with a cross-origin WebSocket endpoint. The Go client can attach the
bearer token but its dependency-free direct WebSocket dial does not traverse an
HTTP proxy; a failed direct upgrade falls back to the normal HTTP transport. The
C client lets libcurl establish DNS, TCP, proxy tunnels, and TLS before using the
connected socket for the WebSocket exchange.

The daemon evaluates a browser `Origin` before transfer admission. The
configured CORS allowlist governs cross-origin WebSocket handshakes; when Fetch
CORS is disabled, `/__ws` still permits only a same-host browser Origin. Native
clients normally omit Origin and remain unaffected.

The WebSocket remains optional. Older daemons omit the three fields, strict
clients use HTTP directly, and Cloudflare compatibility mode never infers this
private Netspeed route from provider behavior.

### 4.2 Warm-connection HTTP fallback

`GET /__ping` and `HEAD /__ping` return `200 OK`, `Content-Length: 0`, and no
body. Arbitrary query labels such as `measId`, `during`, and `seq` are accepted.
The endpoint has no redirect and does not request connection closure.

A client warms one persistent HTTP transport, then issues probes through the
same connection pool. The measured interval therefore excludes repeated DNS,
TCP, QUIC, and TLS setup. The strict Go and native C clients observe connection
reuse and, when `warmConnectionPing` is advertised, discard a cold probe and
retry until the reported probe has `connectionReused=true`; they fail after
three cold attempts rather than mislabeling handshake time as RTT.

The browser cannot bind Fetch to a named socket. It warms the origin pool, uses
unique URLs plus Resource Timing, and discards attempts that visibly incurred
connection setup. `requestStart` to `responseStart` remains usable when browser
privacy controls hide the connection fields because that interval excludes
connection establishment. A manual or `fetchStart` fallback with unobservable
reuse is rejected when warm probing was promised; cross-origin deployments must
therefore preserve `Timing-Allow-Origin`. Browser results label reuse as true,
false, or unobservable, record discarded attempts and observed HTTP protocol,
and never turn unobservable evidence into a claim of socket reuse.

`GET /__down?bytes=0` remains the compatibility fallback when no dedicated HTTP
ping path is advertised. The normalized selection always records
`httpFallbackAvailable: true`, even when WebSocket is preferred.

During loaded-latency measurement, the selected latency transport is used while
the client independently proves that directional load remained continuous for
the full probe interval. Clients reserve one advertised transfer slot for the
latency channel. The browser also reserves one of the conventional six HTTP/1.1
per-origin connection slots so an HTTP fallback is not queued behind its own
throughput workers.

### 4.3 Cloudflare-compatible HTTP latency

Later Cloudflare-mode downloads retain only bounded evidence windows distributed
across the complete response. This catches a random-looking prefix followed by a
compressible body without adding full-stream compression work to the throughput
measurement.

The Go and native C Cloudflare adapters always use a dedicated one-connection
session for `GET /__down?bytes=0`. They prime that session before each idle or
loaded condition, accept only observed connection reuse, and discard up to four
cold attempts per sample. The Go implementation measures
`GotFirstResponseByte - WroteRequest` with `httptrace`; the C implementation uses
libcurl's pre-transfer, start-transfer, and new-connection information. Server
time follows the Cloudflare metric families: a `cfReqDur` total takes precedence,
otherwise `cfSpeed*` component durations are summed, with `app` as a fallback.
JSON records warmup and discarded counts, adjustment counts, method, path,
transport, and observed HTTP protocols. Netspeed's WebSocket route is not
probed or guessed in Cloudflare mode.

## 5. Common measurement response controls

Every ordinary response emitted by `/__down`, `/__up`, and `/__ping`, including
parameter and transfer-admission errors, carries the applicable controls before
the response is committed:

```http
Cache-Control: no-store, no-transform
CDN-Cache-Control: no-store
Surrogate-Control: no-store
Pragma: no-cache
Expires: 0
X-Accel-Buffering: no
X-Content-Type-Options: nosniff
X-Netspeed-Measurement: download|upload|latency
```

The `/__ws` 101 response carries the smaller upgrade-safe set shown in section
4.1. These headers state the daemon's contract and suppress Nginx response buffering
when that upstream header is honored. They do not override a reverse proxy that
is configured to ignore them, cannot disable request-body buffering, and cannot
repair a CDN that transforms traffic before the daemon sees it. Deployment
configuration remains mandatory; see
[`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md#4-reverse-proxying).

## 6. Status behavior

- malformed, conflicting, or out-of-range query discriminators: `400`;
- upload `bytes` above the daemon ceiling or an oversized body: `413`;
- non-identity upload content coding: `415`;
- transfer or quota admission rejection: `429` or `503` under the existing
  service-hardening contract;
- unsupported ordinary HTTP method: `405` with `Allow`;
- malformed WebSocket upgrade or application frame: rejected upgrade or RFC
  6455 protocol close, followed by client HTTP fallback.

No error is converted into a successful short measurement. Once a streamed
response is committed, an interrupted write is logged and the connection ends;
the daemon does not append a textual error body to measured bytes.
