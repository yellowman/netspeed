# netspeed measurement protocol v2

This document is the canonical measurement contract for the Go daemon and the
Go, native C, and browser clients. It supersedes giant-profile, post-transfer
loaded-latency, and short JSON packet examples retained in older design notes.

Service admission capabilities do not change measurement protocol version 2.
Authentication, quotas, trusted client identity, and overload status codes are
defined by [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md). WebRTC ownership and
teardown are defined by [`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md).

## 1. capability negotiation

A version-2 client starts with `GET /meta` and requires these fields:

```json
{
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

- `maxTransferBytes` is the largest individual download or upload request the
  daemon will accept.
- `maxConcurrentTransfersPerClient` is the active request ceiling attributed to
  one resolved client. It must be at least `2`; supported clients reserve one
  slot for a loaded-latency probe and use at most the remainder for load flows.
- `measurementProtocolVersion` must be at least `2`.
- `uploadReceiptVersion` must be at least `1` whenever uploads are requested.
- `packetLossFrameVersion` must be at least `1` for the packet test.
- `measurementCapabilities` is an optional transport-control extension. Version
  1 advertises exact query parameter names, pseudorandom and zero-fill payloads,
  fixed and streamed framing, upload content-coding restrictions, an optional
  persistent WebSocket echo, warm HTTP fallback, and anti-transform/proxy-buffer
  headers. Its complete schema is defined in
  [`HTTP_MEASUREMENT_TRANSPORT.md`](HTTP_MEASUREMENT_TRANSPORT.md).

The strict Go CLI, native C CLI, and browser validate and negotiate this
object, use its advertised paths and parameter names, and publish the normalized
choice as `meta.measurementSelection`. The browser also publishes transport
request, response, payload, and latency evidence under `httpTransport`.
Explicit transport controls fail against an absent, unsafe, incomplete, or
unsupported advertisement.

The Go and native C Cloudflare compatibility paths have no authority to invent
that advertisement. Their `cloudflare-http-v2` contract behaviorally probes the
common `/__down?bytes=N` surface, records the observed provider-default payload
and framing under `httpTransport`, and treats explicit transport flags as
constraints. Neither sends Netspeed-only discriminator keys merely because a
CLI flag was present. The browser's Netspeed path uses the advertised strict
contract; it does not interpret a missing or malformed advertisement as
permission to switch to Cloudflare behavior.

A v2 client does not silently fall back to pre-v2 measurement methodology. A
packet test may be reported as unavailable when its frame capability is missing,
but missing throughput capabilities are fatal to a normal test.

## 2. verified transfer contract

### 2.1 download

A client requests a bounded payload with the compatibility form:

```text
GET /__down?bytes=<decimal>&measId=<unique-id>&profile=<name>&run=<index>
```

When transport capabilities version 1 is advertised, the Go, native C, and
browser clients select `payload=random|zero`, `framing=fixed|chunked`,
`chunkBytes=N`, and `flush=true|false` using the advertised parameter names. Defaults remain
`random`, `fixed`, and the advertised application write size, so existing
clients are unchanged. The full discriminator contract is
in [`HTTP_MEASUREMENT_TRANSPORT.md`](HTTP_MEASUREMENT_TRANSPORT.md).

The client accepts a sample only when:

- the response status is `200`;
- the content type is `application/octet-stream`;
- fixed framing supplies a `Content-Length` equal to the requested byte count;
- streamed framing supplies no `Content-Length`;
- the body contains exactly the requested number of bytes;
- the measured body interval is positive.

`Cache-Control: no-store, no-transform` applies in both modes. A negotiated
strict sample additionally requires matching `X-Netspeed-Measurement`,
`X-Netspeed-Payload`, `X-Netspeed-Framing`, `X-Netspeed-Chunk-Bytes`, and
`X-Netspeed-Flush` headers plus the advertised proxy-buffer suppression value.
The Go and native C clients request `Accept-Encoding: identity` and disable
automatic decompression. Browser scripts cannot set that forbidden request
header, so the browser instead rejects a visible non-identity
`Content-Encoding` and requires the daemon to expose it through CORS.

The Go and native C Cloudflare adapters verify that every throughput body
retains the payload/framing behavior observed by the bounded preflight probe.
Native clients consume bodies without retaining them beyond bounded evidence
windows distributed across the response. The browser uses a `ReadableStream`
when available, samples distributed windows to distinguish zero-fill from
low-diversity data mislabeled as pseudorandom, and otherwise refuses to
materialize more than 100 MB.

### 2.2 upload

A client sends an exact-length binary request with:

```text
POST /__up?bytes=<decimal>&measId=<unique-id>&profile=<name>&run=<index>
Content-Type: application/octet-stream
Content-Encoding: identity
Content-Length: <decimal>
```

The optional `bytes` query value is verified against both a known
`Content-Length` and the actual consumed body. The daemon rejects a request when
its declared or observed body exceeds `maxTransferBytes`, when the body is
shorter than its declared length, when `bytes` does not match, when body reading
fails, or when a non-identity content coding such as gzip or Brotli is supplied.
A successful response is version 1 of the verified receipt:

```json
{
  "ok": true,
  "acceptedBytes": 1000000,
  "serverDurationNs": 123456789
}
```

A client accepts the sample only when `acceptedBytes` equals the query
`bytes`, the declared length when present, and the number of bytes actually
supplied by the client. The canonical upload duration is `serverDurationNs`;
it spans daemon body ingestion only. Precise client body-write timing is a
fallback only when the receipt duration is unavailable. Successful and error
responses use `Cache-Control: no-store, no-transform` and advertise identity
content handling through diagnostic headers.

The Go and native C clients generate upload bytes from bounded streaming
generators. A browser
with request-stream support emits 64 KiB chunks. Other browsers reuse one payload
no larger than 8 MiB and use XHR upload events to track the outbound interval.

## 3. throughput methodology

### 3.1 baseline probes

Each enabled direction first runs three verified 100 kB requests and three
verified 1 MB requests. At least two of three requests in each group must
succeed. Baselines tune the sustained-window plan; they do not contribute to the
headline speed when window samples exist.

### 3.2 bounded request plan

The median 1 MB baseline speed selects a request chunk intended to last roughly
250 ms per flow:

```text
target bytes = estimated bits/s / 8 × 0.250 s / concurrency
```

The result is rounded up to 64 KiB and bounded by:

- minimum chunk: 100,000 bytes;
- implementation maximum chunk: 256 MiB;
- daemon maximum: `maxTransferBytes`;
- browser non-streaming download fallback: 100 MB;
- browser non-streaming upload fallback: 8 MiB.

Concurrency increases instead of allowing unbounded requests. Each nominal
flow count is additionally capped at `maxConcurrentTransfersPerClient - 1` so a
loaded-latency probe always has an admission slot:

| estimated rate | Go flow count | browser flow count |
|---:|---:|---:|
| below 100 Mbps | 1 | 1 |
| 100–499 Mbps | 2 | 2 |
| 500–1,999 Mbps | 4 | 4 |
| 2–9.999 Gbps | 8 | 5 |
| 10 Gbps or more | 16 | 5 |

The browser limit is intentionally lower to control connection and main-thread
pressure and to leave one of the conventional six HTTP/1.1 per-origin
connections available for the loaded-latency probe. Each worker repeatedly
performs complete, independently verified requests until the window owner stops
it.

### 3.3 fixed-duration windows

A normal test runs three 1.5-second windows in each enabled direction. A quick
test runs one 1-second window. A window sample is the aggregate:

```text
window Mbps = verified completed bytes × 8 / elapsed wall-clock seconds / 1,000,000
```

The elapsed interval begins before workers are released and ends after workers
have stopped and all in-flight requests have returned. Only fully verified
requests contribute bytes. A window with no verified request fails.

Headline download and upload speeds are the R-7 p90 of valid window samples
after the shared conservative IQR filter. Baseline samples are used only as a fallback
for old internally constructed result fixtures, not normal v2 runs.

## 4. latency and continuous loaded overlap

### 4.1 unloaded latency

A normal run requests 20 latency probes; quick mode uses fewer probes according
to the client configuration. A client prefers WebSocket only when the capability
object supplies the exact same-origin path, `netspeed.ping.v1` subprotocol, and
16-byte payload contract. It establishes one persistent connection, sends one
unreported warmup message, and then measures from each binary send to the exact
nonce echo. DNS, TCP, TLS, HTTP Upgrade, and warmup time are outside the sample.

The application payload is `NSP1`, a big-endian 32-bit sequence number, and an
8-byte random nonce. Text, fragmented, malformed, oversized, or mismatched echo
messages are protocol failures. After the first upgrade, timeout, close,
subprotocol, framing, or echo failure, the client disables WebSocket for the
remainder of the run and permanently uses the advertised warm HTTP path. The
fallback reason is attached to later HTTP samples rather than retried on every
probe.

HTTP uses a zero-body `GET` or `HEAD` to the advertised `/__ping` path, or
`GET /__down?bytes=0` when no dedicated path exists. RTT is request-write
completion to first response byte when precise timing is available. The Go and
native C clients warm one transport, require observed connection reuse when the
server promises it, and discard up to three cold attempts rather than reporting
handshake-contaminated latency.

The browser cannot send native WebSocket control pings, inspect upgrade headers,
attach an `Authorization` header, or reproduce every Fetch cookie-suppression
mode. It therefore uses the same application echo, validates the selected
subprotocol and nonce, and starts with HTTP when a bearer token is configured,
credentials are `omit`, or `same-origin` credentials are paired with a
cross-origin API. On HTTP it warms the origin pool and uses Resource Timing. A
visible connection setup is discarded; hidden setup fields can yield
`connectionReused: null` only when `requestStart` to `responseStart` itself
excludes setup. Cross-origin HTTP deployments must preserve
`Timing-Allow-Origin`.

Reduced browser timer precision can make an otherwise valid, very fast
WebSocket echo produce a raw zero-tick interval. The browser records
`rawRttMs: 0`, `timingResolutionLimited: true`, and a documented positive
`rttMs` representation floor of 0.01 ms so the sample remains visible to the
positive-only statistics pipeline. Negative and non-finite intervals are still
errors and trigger the normal permanent HTTP fallback.

Accepted samples label `probeTransport`, `probeMethod`, `probePath`,
`webSocketProtocol`, and any `probeFallbackReason`. Cloudflare compatibility
mode remains HTTP-only and never infers the private Netspeed WebSocket path.
Warmup removal drops the first two valid unloaded samples before summary
statistics are computed.

### 4.2 loaded latency

The middle throughput window owns the loaded-latency probes in a normal run. The
single quick-mode window owns them in quick mode.

All supported clients maintain an aggregate active-transfer count and a monotonically
increasing gap generation. A loaded probe is accepted only when:

1. at least one measured transfer is active before the probe starts;
2. at least one measured transfer is active after the probe finishes; and
3. the gap generation did not change during the probe.

Any probe that crosses even a brief zero-active interval is rejected and retried.
Download activity spans response-body consumption. Upload activity begins with
request-body transmission and remains active through verified receipt completion.

Normal mode targets five loaded probes during each direction's selected window
and requires at least three accepted overlap-proven probes. Quick mode targets
three. The configured window timer stops new transfer requests and further
probe retries; already in-flight transfers are allowed to drain and remain in
the aggregate. If the timer expires after the minimum probe quorum has been
accepted, loaded latency succeeds with that quorum. Otherwise the window fails
instead of extending its nominal duration indefinitely.

## 5. shared statistics

The Go and browser clients use the same definitions:

- retain finite, positive measurements;
- remove the configured warmup samples before latency filtering;
- calculate percentiles with the R-7 interpolation rule;
- apply a 1.5-IQR fence only when it leaves a useful sample set, otherwise keep
  the original set;
- headline throughput: R-7 p90 of window Mbps values;
- latency: median of the filtered set;
- jitter: p90 minus median;
- variability: population standard deviation divided by the mean, expressed as
  a percentage.

The shared implementations are in `internal/measurement/stats.go` and the
matching functions in `web/js/speedtest.js`.

## 6. exact-size packet protocol

### 6.1 transport and semantics

The client opens an unordered WebRTC data channel with `maxRetransmits: 0` and
sends exact 1,200-byte SCTP user messages. The daemon acknowledges each unique,
valid probe once. Duplicate probes are counted but not acknowledged again.

The packet test uses TURN relay candidates in the current implementation.
Session ownership, disconnect recovery, and teardown are defined by
[`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md). Public-service limits and TURN
hardening are defined by
[`SERVICE_HARDENING.md`](SERVICE_HARDENING.md).

### 6.2 frame layout

All multi-byte integers are big-endian.

| offset | size | field |
|---:|---:|---|
| 0 | 4 | ASCII magic `NSPL` |
| 4 | 1 | frame version, currently `1` |
| 5 | 1 | type: `1` probe, `2` acknowledgement |
| 6 | 2 | header size, `32` |
| 8 | 4 | sequence number |
| 12 | 8 | client send time, Unix milliseconds |
| 20 | 8 | daemon receive time, Unix milliseconds; zero in probes |
| 28 | 4 | declared frame size, `1200` |
| 32 | 1168 | deterministic sequence-derived padding |

The decoder rejects the wrong byte length, magic, version, header length,
declared size, type, or padding. Local `send()` failures do not consume a
sequence number and therefore are not classified as network loss.

### 6.3 authoritative report

After collecting acknowledgements, the client sends its transaction result to:

```text
POST /api/packet-test/report
```

The daemon snapshots the active session before closing it and returns:

```json
{
  "ok": true,
  "protocolVersion": 2,
  "frameSizeBytes": 1200,
  "forwardReceived": 99,
  "acknowledgementsSent": 99,
  "duplicateFrames": 0,
  "invalidFrames": 0,
  "ackSendFailures": 0
}
```

The three loss metrics are:

```text
transaction loss = (probes sent - unique acknowledgements received) / probes sent
forward loss = (probes sent - unique valid probes received by daemon) / probes sent
reverse acknowledgement loss =
    (acknowledgements sent by daemon - unique acknowledgements received by client)
    / acknowledgements sent by daemon
```

Transaction loss is the backward-compatible headline. It combines both network
directions. Forward and reverse acknowledgement loss are separately reported
from reconciled client and daemon counters. Reverse loss is unavailable when the
daemon sent no acknowledgement.

## 7. confidence gates

The clients calculate the same 0–100 confidence score from five visible gates:

| gate | requirement | deduction |
|---|---|---:|
| sample adequacy | 3 windows and 3 overlap-proven loaded probes per enabled direction; at least 10 unloaded probes | 20 |
| variability | throughput CV below 30%; unloaded-latency CV below 50% | 25 |
| loaded overlap | required loaded probes have proven continuous overlap | 25 |
| packet test | directional packet report completed and reverse-ACK loss is measurable | 20 |
| timing accuracy | no measurement used an imprecise timing fallback | 10 |

Scores of 80–100 are `high`, 50–79 are `medium`, and lower scores are `low`.
Unavailable packet loss remains `null`/`N/A`; it is never converted to zero and
causes application grades that depend on loss to be `Incomplete`.

## 8. service-admission responses

Servers may reject a transfer before measurement work begins:

- `429 Too Many Requests` for a per-client transfer ceiling or byte quota;
- `503 Service Unavailable` for the global transfer ceiling;
- `Retry-After` on admission and quota rejections.

These responses are not samples. Clients discard them and fail the affected
window rather than measuring the small error body. Authentication failures are
also not samples and return `401` as defined by `SERVICE_HARDENING.md`.

## 9. implementation and compatibility boundaries

- The Go CLI, native C CLI, and browser are supported v2 clients in this
  archive. All three use the same verified-transfer, fixed-window,
  loaded-overlap, statistics, and directional packet-result contract.
- The C implementation and its libdatachannel release boundary are documented
  in [`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md).
- [`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md) defines session ownership,
  cancellation, disconnect recovery, and race-safe teardown.
- [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md) defines public-service
  concurrency, rate, quota, authentication, trusted-proxy, metrics, and TURN
  controls.
- [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md) defines endpoint deadlines, browser
  API routing, CORS and Resource Timing, middleware recovery, shutdown ordering,
  TLS, configuration, and GeoIP behavior.
- [`RELEASE_QUALIFICATION.md`](RELEASE_QUALIFICATION.md) defines the real
  dependency CI matrix, process and browser fixtures, embedded-TURN
  interoperability, and deterministic release construction.
- [`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md) defines native C protocol parity,
  C-to-Pion/TURN interoperability, portable pmake builds, and release treatment.
