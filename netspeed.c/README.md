# Legacy C client — unsupported compatibility source

The program in this directory is retained for reference and source portability
checks. It is **not** an official Netspeed release client.

The supported client is the Go program in `cmd/netspeed`. That client implements
the measurement protocol documented in `MEASUREMENT_PROTOCOL_V2.md`, including
verified upload receipts, sustained fixed-duration load, overlap-qualified
loaded latency, and exact-size packet-loss reconciliation.

The C implementation predates that protocol and therefore must not be used to
compare or publish authoritative results from a current `netspeedd` server. It is excluded from binary release archives and official binaries. Its source
remains in the full source archive for historical and portability work. CI only
verifies that it continues to compile cleanly with GCC and Clang using warnings
as errors.

## Source-health build

```sh
make check-compilers
```

A single-toolchain build remains available for developers inspecting the legacy
implementation. The Makefile requires GNU Make; use `gmake` on OpenBSD and the
other BSDs when base `make` is not GNU Make.

```sh
make
./netspeed --version
make clean
```

Promoting this client back into the supported release would require a complete
protocol-v2 migration and end-to-end interoperability qualification; a clean
compiler build alone is not sufficient.
