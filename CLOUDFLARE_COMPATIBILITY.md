# Cloudflare Compatibility

Netspeed's strict protocol remains the default authority. A server that exposes recognizable Netspeed metadata is always handled as Netspeed; incompatible metadata is an error and is never silently downgraded.

The Go and C clients accept `--provider auto`, `--provider netspeed`, and `--provider cloudflare`.

- `netspeed` requires protocol-v2 metadata, exact download counts, and a matching upload receipt.
- `cloudflare` uses `/__down?bytes=N` and `/__up?bytes=N`. Downloads remain exactly counted. Uploads are accepted only when the local HTTP transport consumes the complete body and the endpoint returns success, and the result is labeled `client-observed-complete-body`.
- `auto` uses Cloudflare mode only after a Cloudflare hostname or response-header fingerprint. Otherwise the strict Netspeed client remains in control.

Cloudflare packet testing uses two local WebRTC peers, relay-only ICE, UDP TURN, an unordered data channel named `channel`, and zero retransmissions. The result topology is `turn-loopback`. Netspeed's normal packet test remains `server-peer` and retains authoritative directional counters.

TURN credentials may be supplied as a Cloudflare Realtime-style `iceServers` object, `urls`, or the `server`/`username`/`credential` form. Native clients also accept direct TURN options.


## Daemon HTTP transport extension

A Netspeed daemon additionally advertises transport-controls version 1. Its
`/__down` endpoint accepts `payload=random|zero`, `framing=fixed|chunked`,
`chunkBytes=N`, and `flush=true|false` while preserving Cloudflare-compatible
`bytes=N` defaults. `POST /__up?bytes=N` verifies the discriminator and rejects
compressed request bodies. `GET` or `HEAD /__ping` provides a dedicated
zero-body warm-connection latency path; `GET /__down?bytes=0` remains the
compatibility fallback.

Existing Cloudflare provider mode continues to use the common default surface.
Capability-aware selection by the Go, C, and browser clients is intentionally a
separate adoption step; an unrecognized `measurementCapabilities` object does
not make a protocol-v2 server incompatible.
