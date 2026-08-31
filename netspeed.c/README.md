# Netspeed native C client

This directory contains the first-class native C implementation of Netspeed
measurement protocol v2. It is intended to produce results equivalent to the Go
`cmd/netspeed` client while using libcurl for HTTP and libdatachannel for the
exact WebRTC packet test.

The canonical feature and qualification matrix is
[`../C_CLIENT_PARITY.md`](../C_CLIENT_PARITY.md). The wire and statistical
contract is [`../MEASUREMENT_PROTOCOL_V2.md`](../MEASUREMENT_PROTOCOL_V2.md).

## Dependencies

Required for throughput and latency:

- a C11 compiler;
- POSIX threads;
- libcurl development files;
- zlib development files;
- pkg-config or `curl-config`.

Required for the exact 1,200-byte packet-loss test:

- libdatachannel with its C API headers and link metadata.

## Build

The Makefile works with GNU make and BSD pmake.

```sh
# Auto-detect libdatachannel.
make

# Require the complete packet-test build.
make WEBRTC=yes

# Explicit HTTP-only build; packet loss is reported unavailable.
make WEBRTC=no
```

The resulting executable is `./netspeed`. The repository-level build copies it
to `bin/netspeed-c` so it can coexist with the Go client.

```sh
../bin/netspeed-c --quick http://localhost:8080

# Negotiate low-CPU streamed zero-fill with a Netspeed daemon.
../bin/netspeed-c --provider netspeed \
  --download-payload zero --download-framing chunked \
  --download-chunk-bytes 65536 --download-flush=false \
  http://localhost:8080

# Require a Cloudflare-compatible endpoint's observed default to be random.
../bin/netspeed-c --provider cloudflare \
  --download-payload random https://speed.cloudflare.com
```

## HTTP transport controls

Strict Netspeed mode validates `/meta.measurementCapabilities`, follows its
advertised same-origin paths and query names, and exposes the normalized result
as `meta.measurementSelection`. The following options select or constrain the
HTTP measurement transport:

```text
--download-payload auto|random|zero
--download-framing auto|fixed|chunked
--download-chunk-bytes N
--download-flush auto|true|false
```

Explicit values fail when the daemon does not advertise support. Measurement
requests disable content decoding, request identity coding and
`no-store, no-transform`, and verify the daemon's payload, framing, chunk,
upload-byte, cache-control, and proxy-buffer diagnostics. Latency uses the
advertised zero-byte endpoint and reports only a reused keep-alive probe when
the daemon promises warm-connection support.

Cloudflare mode uses `cloudflare-http-v2`. It behaviorally probes the common
`/__down?bytes=N` endpoint and treats the same options as requirements on the
observed provider defaults. It never sends Netspeed-only discriminator query
keys to force a choice. Idle and loaded latency use dedicated persistent
libcurl sessions; cold attempts are discarded and reuse evidence is included in
JSON output.

## Qualification

```sh
# GCC and Clang, protocol units, and protocol-v2 process fixtures.
make check-compilers

# AddressSanitizer and UndefinedBehaviorSanitizer.
make sanitize

# Compare Go and C process results from the repository root.
make -C .. test-parity WEBRTC=no

# Require libdatachannel for exact-frame compilation and units.
make WEBRTC=yes WEBRTC_STATIC=yes protocol-check
```

The repository integration gate runs the complete C client against the real Go
daemon and embedded Pion TURN relay:

```sh
make -C .. c-client WEBRTC=yes WEBRTC_STATIC=yes
NETSPEED_C_CLIENT="$PWD/../bin/netspeed-c" \
NETSPEED_E2E_TURN=1 \
make -C .. integration-c-turn
```

`WEBRTC=no` is useful for platform bring-up and HTTP-core qualification, but an
official C release binary must be built and tested with `WEBRTC=yes`.
