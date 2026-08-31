# HTTP measurement transport discriminators

This document defines version 1 of Netspeed's optional HTTP transport-control
extension. It extends measurement protocol v2 without changing the verified
byte-count, receipt, window, or statistics rules in
[`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md).

The daemon implements this contract and advertises it in `/meta`. Clients that
do not understand `measurementCapabilities` remain compatible: the default
`/__down?bytes=N` and `/__up` behavior is unchanged. A WebSocket latency path is
optional and is not advertised by the daemon until one is available; clients
must retain HTTP fallback.

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

### 1.1 Go client negotiation and safety

The Go CLI validates the complete object before issuing a measurement request.
Endpoint values must be clean, same-origin relative paths with no authority,
query, fragment, traversal, or backslash form. Download query parameter names
must be syntactically safe and distinct, so hostile or malformed metadata cannot
redirect traffic or collapse two discriminators onto one key.

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

For every run, the normalized contract is included in JSON metadata as
`measurementSelection`. It records the selected payload, framing, application
chunk size, flush behavior, upload encoding, latency path and method, warm-reuse
requirement, and whether legacy fallback was used. The Go client also verifies
the response's measurement type, payload, framing, chunk size, exact length,
identity content coding, and `no-store, no-transform` controls.

### 1.2 Go Cloudflare behavioral negotiation

Cloudflare compatibility cannot assume the daemon's capability object exists.
The Go adapter therefore uses a bounded 64 KiB request to the common
`/__down?bytes=N` surface and records the observed provider defaults under
`httpTransport`:

- binary zero-fill, ASCII-zero, incompressible random, or opaque payload;
- exact-length fixed, HTTP/1.x chunked, or lengthless streamed framing;
- optional exact chunk and flush evidence from `X-Netspeed-Chunk-Bytes` and
  `X-Netspeed-Flush`.

The adapter identifies this behavior as `cloudflare-http-v2`. `auto` accepts the
observed defaults. Explicit CLI values are requirements: they succeed only when
the probe proves the endpoint already behaves that way. No `payload`, `framing`,
`chunkBytes`, or `flush` query is sent to force an unadvertised choice. Every
later download is checked for drift from the probe.

All requests send `Accept-Encoding: identity`, `Cache-Control: no-store,
no-transform`, and `Pragma: no-cache`; uploads also send `Content-Encoding:
identity`. Automatic decompression is disabled and any non-identity response
coding is fatal. The probe reports, but cannot manufacture, remote response
controls such as `no-transform` or `X-Accel-Buffering: no`.

The native C and browser adoption is intentionally handled in later client
phases; they continue to use the compatible default endpoint surface in this
tree.

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

## 4. Warm-connection HTTP latency

`GET /__ping` and `HEAD /__ping` return `200 OK`, `Content-Length: 0`, and no
body. Arbitrary query labels such as `measId`, `during`, and `seq` are accepted.
The endpoint has no redirect and does not request connection closure.

A client should warm one persistent HTTP transport, then issue probes through the
same connection pool. The measured interval therefore excludes repeated DNS,
TCP, QUIC, and TLS setup. The strict Go client traces connection acquisition and,
when `warmConnectionPing` is advertised, discards a cold probe and retries until
the reported probe has `connectionReused=true`; it fails after three cold
attempts rather than mislabeling handshake time as RTT. During loaded-latency
measurement, the same endpoint is used while the client independently proves
that directional load remained continuous for the full probe interval.

Later Cloudflare-mode downloads retain only bounded evidence windows distributed
across the complete response. This catches a random-looking prefix followed by a
compressible body without adding full-stream compression work to the throughput
measurement.

The Go Cloudflare adapter always uses a dedicated one-connection pool for
`GET /__down?bytes=0`. It primes that pool before each idle or loaded condition,
accepts only `GotConnInfo.Reused=true`, and discards up to four cold attempts per
sample. Its RTT interval is `GotFirstResponseByte - WroteRequest`. Server time
follows the Cloudflare metric families: a `cfReqDur` total takes precedence,
otherwise `cfSpeed*` component durations are summed, with `app` as a fallback.
JSON records warmup and discarded counts, adjustment counts, method, path,
transport, and observed HTTP protocols.

`GET /__down?bytes=0` remains a compatible fallback. A future WebSocket ping
path may be advertised through `webSocketPingPath`; its absence means the client
must use HTTP rather than treating latency as unavailable.

## 5. Common measurement response controls

Every response emitted by the `/__down`, `/__up`, and `/__ping` handlers,
including parameter and transfer-admission errors, carries the applicable
controls before the response is committed:

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

These headers state the daemon's contract and suppress Nginx response buffering
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
- unsupported method: `405` with `Allow`.

No error is converted into a successful short measurement. Once a streamed
response is committed, an interrupted write is logged and the connection ends;
the daemon does not append a textual error body to measured bytes.
