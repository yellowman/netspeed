# Cloudflare compatibility

Netspeed's strict protocol remains the default authority. A server that exposes
recognizable Netspeed metadata is always handled as Netspeed; incompatible
metadata is an error and is never silently downgraded.

The Go and C clients accept `--provider auto`, `--provider netspeed`, and
`--provider cloudflare`.

- `netspeed` requires protocol-v2 metadata, exact download counts, and a
  matching upload receipt.
- `cloudflare` uses the common `/__down?bytes=N` and `/__up?bytes=N` surface.
  Downloads remain exactly counted. Uploads are accepted only when the local
  HTTP transport consumes the complete body and the endpoint returns success,
  and are labeled `client-observed-complete-body`.
- `auto` enters Cloudflare mode only after a Cloudflare hostname or response
  fingerprint. Otherwise the strict Netspeed client remains in control.

Cloudflare packet testing uses two local WebRTC peers, relay-only ICE, UDP TURN,
an unordered data channel named `channel`, and zero retransmissions. Its result
topology is `turn-loopback`. Netspeed's packet test remains `server-peer` and
retains authoritative directional counters.

TURN credentials may be supplied as a Cloudflare Realtime-style `iceServers`
object, `urls`, or the `server`/`username`/`credential` form. Native clients also
accept direct TURN options.

## Native-client Cloudflare HTTP contract v2

The Go and native C compatibility adapters identify their strengthened HTTP
behavior as `cloudflare-http-v2`. They do not assume that Cloudflare's common
endpoint supports Netspeed's optional `payload`, `framing`, `chunkBytes`, or
`flush` query parameters.

Before a run, the client requests a bounded 64 KiB download using only the
common `bytes` discriminator and observes the endpoint's provider defaults:

- payload is classified as binary `zero`, `ascii-zero`, incompressible
  `random`, or `opaque` from body evidence;
- framing is classified as exact-length `fixed`, HTTP/1.x `chunked`, or
  lengthless `streamed` framing;
- application chunk size and flush behavior are considered known only when the
  endpoint supplies exact `X-Netspeed-Chunk-Bytes` or `X-Netspeed-Flush`
  evidence.

The four transport CLI options work as constraints in Cloudflare mode:

```text
--download-payload auto|random|zero
--download-framing auto|fixed|chunked
--download-chunk-bytes N
--download-flush auto|true|false
```

`auto` accepts and labels the observed provider default. An explicit value
continues only when the probe proves that the endpoint already behaves that
way. Otherwise the command exits with code 2. The adapter never tries to force
the choice by sending Netspeed-only query keys to an endpoint that did not
advertise them. Every later download is checked for payload and framing drift
using bounded windows distributed across the complete response; verified
chunk/flush evidence must remain stable as well.

JSON output records this under `httpTransport`, including:

- `capabilitySource: "behavioral-probe"`;
- `providerDefaultsOnly: true`;
- `queryDiscriminatorsSent: false`;
- requested and selected payload/framing/chunk/flush values and evidence;
- download, upload, latency, and byte-parameter names;
- request and response anti-transformation evidence.

## Compression and proxy controls

Every native Cloudflare measurement request sends:

```http
Accept-Encoding: identity
Cache-Control: no-store, no-transform
Pragma: no-cache
```

Uploads additionally send:

```http
Content-Type: application/octet-stream
Content-Encoding: identity
Content-Length: <exact bytes>
```

Automatic HTTP decompression is disabled. A response carrying a non-identity
`Content-Encoding`, or one already transparently decompressed by the HTTP
transport, is rejected rather than used for throughput. The probe records
whether the endpoint itself returned `no-store`, `no-transform`, and
`X-Accel-Buffering: no`. Their absence is reported as missing evidence because a
client cannot retroactively change a remote proxy's response-buffer policy.

The compatibility upload stream remains ASCII `0`, matching the common
Cloudflare client behavior. It is reported separately as
`uploadPayload: "ascii-zero"`; it is not confused with the daemon's binary
zero-fill download mode.

## Warm HTTP latency and loaded jitter

Cloudflare-mode latency uses `GET /__down?bytes=0` on a dedicated transport
limited to one connection. Each idle, download-loaded, and upload-loaded
session:

1. issues an unreported warmup request;
2. observes request completion, first response byte, and whether the request
   opened a new connection (`net/http/httptrace` in Go and libcurl connection
   information in C);
3. accepts only a probe whose connection was reused;
4. discards and retries cold attempts up to four times;
5. computes the request-to-first-byte interval: Go uses
   `GotFirstResponseByte - WroteRequest`, while C uses libcurl's
   pre-transfer-to-start-transfer interval;
6. prefers a Cloudflare `cfReqDur` total, otherwise sums `cfSpeed*`
   components, with a generic `app` duration only as a final fallback.

This keeps DNS, TCP, and TLS setup out of reported RTT. The loaded probe
transport is separate from the throughput pool, so the latency connection is
warmed before load begins and cannot accidentally become a new throughput
connection.

Each latency result records `connectionReused`, `warmSamples`,
`warmupRequests`, `discardedColdAttempts`, `serverTimingAdjustedSamples`,
`probeTransport`, `probeMethod`, `probePath`, and the observed HTTP protocols.
A server that closes every response produces unavailable latency rather than a
handshake-contaminated number.

## Netspeed daemon transport extension

A Netspeed daemon advertises transport-controls version 1. Its `/__down`
endpoint accepts `payload=random|zero`, `framing=fixed|chunked`, `chunkBytes=N`,
and `flush=true|false` while preserving Cloudflare-compatible `bytes=N`
defaults. `POST /__up?bytes=N` verifies the byte discriminator and rejects
compressed request bodies. `GET` or `HEAD /__ping` provides a dedicated
zero-body warm-connection path; `GET /__down?bytes=0` remains the compatibility
fallback.

The strict Go and native C Netspeed paths validate and negotiate that
advertisement, including custom same-origin paths and parameter names. Their
normalized choice is exposed as `meta.measurementSelection`.

The browser Netspeed client validates and consumes this same advertisement,
including custom paths and parameter names, and exposes its normalized choice
under `meta.measurementSelection` and `httpTransport`. Browser Cloudflare
behavioral probing is not inferred from a missing or malformed Netspeed
advertisement; an unrecognized object never makes a protocol-v2 server eligible
for silent downgrade.
