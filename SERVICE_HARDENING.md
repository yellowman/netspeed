# Netspeed service hardening and operational controls

This document is the canonical contract for exposing `netspeedd` to untrusted
clients. It supplements the measurement contract in
[`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md) and the session
ownership contract in [`WEBRTC_LIFECYCLE.md`](WEBRTC_LIFECYCLE.md).

The daemon rejects overloaded work immediately. Measurement requests are never
queued behind an admission limit because queueing would corrupt throughput and
latency timing.

## 1. bounded defaults

The daemon starts with these resource controls even when bearer authentication
is not configured:

| control | default |
|---|---:|
| maximum bytes in one download or upload | 1 GiB |
| active measurement transfers, global | 256 |
| active measurement transfers, per client | 24 |
| transferred bytes per client per fixed window | 1 TiB per hour |
| active WebRTC sessions, global | 64 |
| active WebRTC sessions, per client | 2 |
| WebRTC offers per client | 12/minute, burst 4 |
| ICE-configuration requests per client | 60/minute, burst 10 |
| WebRTC offer JSON body | 128 KiB |
| packet-report JSON body | 16 KiB |
| maximum TURN credential lifetime | 600 seconds |
| embedded TURN | disabled |
| embedded TURN listen address when enabled | `127.0.0.1:3478` |
| embedded TURN combined UDP ceiling | 100 Mbps |
| Prometheus metrics | disabled |

A byte quota of `0` disables the byte quota. A WebRTC-offer or
ICE-configuration rate of `0` disables that rate limiter. Concurrency and body
limits must remain positive. The per-client transfer limit must be at least two
because a loaded-latency test needs one traffic flow and one probe slot.

The quota and rate-limit state is held in memory and is bounded to 65,536 client
keys per limiter. It is reset when the daemon restarts.

## 2. client identity and trusted proxies

Admission, quota, rate, session-ownership, and request-log decisions use one
resolved client IP.

Forwarding headers are ignored unless the direct TCP peer belongs to a configured
trusted proxy CIDR. When trusted proxies are configured, the daemon walks
`X-Forwarded-For` from the trusted edge toward the origin and selects the first
untrusted hop. A malformed or partially malformed chain is rejected as a whole.
`CF-Connecting-IP` and `X-Real-IP` are accepted only as single valid IP addresses
from a trusted direct peer.

Configure the complete proxy path, not the public client networks:

```bash
NETSPEEDD_TRUSTED_PROXY_CIDRS='127.0.0.0/8,10.0.0.0/8,192.0.2.0/24' \
  ./netspeedd
```

The legacy `NETSPEEDD_TRUST_PROXY=true` or `-trust-proxy=true` switch is only an
enablement guard. Validation fails unless trusted CIDRs are also supplied.

Clients sharing a NAT address also share the per-client ceilings and quota.
Deployments that require per-user accounting should authenticate upstream and
apply principal-aware policy there; the built-in shared bearer token is not
an identity provider.

## 3. bearer authentication

Set `NETSPEEDD_ACCESS_TOKEN` or `-access-token` to require:

```http
Authorization: Bearer <token>
```

The token must contain at least 16 bytes. Comparison is constant-time after
scheme and length validation.

Protected routes are:

- `/meta`, `/__down`, `/__up`, `/locations`, and `/cdn-cgi/trace`;
- every `/api/` route, including ICE configuration and packet-test signaling.

`/health` and static web assets remain public. CORS preflight requests are
answered before authentication. An unauthorized service request receives `401`,
`WWW-Authenticate: Bearer realm="netspeed"`, and `Cache-Control: no-store`.

Successful `/locations` responses are also `Cache-Control: private, no-store`
with `Pragma: no-cache` and `Vary: Authorization`. This is part of the authentication boundary: a shared
reverse-proxy or CDN cache must never reuse an authenticated locations response
for a later request that did not reach the daemon's authentication middleware.
`Authorization` is not treated as a substitute cache key, and protected
responses must not be changed back to `public` caching.

The Go client accepts either:

```bash
./netspeed --token "$NETSPEED_TOKEN" https://speed.example.com
```

or the `NETSPEED_TOKEN` environment variable.

The browser measurement engine reads `globalThis.NETSPEED_CONFIG.accessToken`.
It must be set before `speedtest.js` loads:

```html
<script>
  window.NETSPEED_CONFIG = {
    accessToken: "replace-with-a-deployment-token"
  };
</script>
<script src="js/speedtest.js"></script>
```

A token embedded in browser-delivered HTML or JavaScript is visible to that
browser. This mode is suitable for controlled installations or a token supplied
by an authenticated upstream application, not for hiding a long-lived secret in
a public static site.

## 4. transfer admission and byte quotas

`GET /__down` and `POST /__up` acquire a global and per-client transfer slot
before performing measurement work.

| rejection | status | retry behavior |
|---|---:|---|
| per-client transfer ceiling | `429` | `Retry-After: 1` |
| global transfer ceiling | `503` | `Retry-After: 1` |
| per-client byte quota | `429` | `Retry-After` until the fixed window resets |

Known download and upload lengths reserve their complete byte count before the
transfer starts. Unknown-length uploads are charged as bytes are consumed and
are stopped once the current quota is exhausted. Reservations are intentionally
not refunded after a disconnect: an aborted request still consumed server and
network resources.

Successful known-size reservations expose
`X-Netspeed-Quota-Remaining-Bytes` when the byte quota is enabled.

`/meta` advertises:

```json
{
  "maxTransferBytes": 1073741824,
  "maxConcurrentTransfersPerClient": 24,
  "measurementProtocolVersion": 2,
  "uploadReceiptVersion": 1,
  "packetLossFrameVersion": 1
}
```

Both supported clients cap their concurrency to the advertised per-client
ceiling. Sustained-load testing reserves one slot for a loaded-latency probe, so
at most `maxConcurrentTransfersPerClient - 1` traffic flows run at once.

## 5. WebRTC admission and report ownership

The WebRTC manager reserves global and per-client capacity before allocating a
Pion peer connection. Capacity includes offers still negotiating, so concurrent
requests cannot exceed the configured ceiling through a signaling race.

Offer creation has a separate per-client token bucket. A client-capacity
rejection returns `429`; global capacity or manager shutdown returns `503`.
Both include a short `Retry-After` value. Unexpected signaling failures are
logged server-side while clients receive a generic `500` response.

Every successfully registered packet-test session is bound to the resolved
client identity that created it. `POST /api/packet-test/report` atomically checks
that identity before removing, snapshotting, and closing the session. A missing
session and a session owned by another client both return `404`, avoiding an
ownership oracle.

Packet reports must also be internally consistent:

- `sent` is between 1 and 100,000;
- `received` is between zero and `sent`;
- `lossPercent` matches the counts within 0.1 percentage point;
- RTT and jitter values are finite values from 0 through 60,000 ms;
- RTTs satisfy minimum <= median <= p90;
- jitter equals p90 minus median within 0.1 ms;
- all RTT values are zero when no acknowledgement was received.

## 6. bounded control-plane bodies

WebRTC offer and packet-report endpoints require
`Content-Type: application/json`. The daemon accepts exactly one JSON value and
rejects trailing JSON or non-whitespace data.

| condition | status |
|---|---:|
| unsupported or missing JSON content type | `415` |
| known or observed body exceeds endpoint limit | `413` |
| malformed JSON or more than one JSON value | `400` |

The limits are independent of the much larger measurement-transfer ceiling.

## 7. ICE and TURN hardening

`GET /api/turn/credentials` is an ICE-configuration endpoint and has a
per-client token bucket. The endpoint supports two configurations:

- **STUN-only:** one or more `stun:` or `stuns:` URLs; the response contains the
  server list and blank username/credential fields. The bundled v2 packet-test
  clients intentionally force relay transport and therefore report packet loss
  as unavailable for a STUN-only list; the response remains useful to custom
  direct-ICE clients.
- **TURN:** one or more `turn:` or `turns:` URLs plus a shared TURN secret. The
  daemon returns a random-suffixed, time-limited username and an HMAC-SHA1
  credential. Requested lifetime is clamped to 60 seconds through
  `NETSPEEDD_MAX_TURN_TTL`.

Embedded TURN is opt-in. Its safe local default is:

```bash
./netspeedd \
  -embedded-turn \
  -embedded-turn-addr 127.0.0.1:3478
```

A non-loopback embedded listener requires an explicit advertised relay IP:

```bash
NETSPEEDD_TURN_SECRET='at-least-16-random-bytes' \
  ./netspeedd \
  -embedded-turn \
  -embedded-turn-addr 0.0.0.0:3478 \
  -embedded-turn-ip 198.51.100.20 \
  -embedded-turn-max-mbps 100
```

If no embedded secret is supplied, a random 256-bit process-lifetime secret is
generated and is never logged. The embedded listener validates the credential
realm, expiry, and maximum future lifetime. Its UDP socket applies one combined
inbound/outbound byte token bucket and exposes accepted and rejected byte
counters through metrics.

Embedded TURN and user-configured external ICE server URLs are mutually
exclusive. A TURN URL without a shared secret, an orphaned shared secret, an
invalid URL scheme, a short secret, or a non-loopback embedded listener without
an advertised IP fails startup validation.

The embedded server supports UDP relay only. TCP/TLS relay listeners and
distributed quota coordination are outside its scope.

## 8. operational metrics

Metrics are opt-in and cannot be enabled without a token:

```bash
NETSPEEDD_ENABLE_METRICS=true \
NETSPEEDD_METRICS_TOKEN='separate-metrics-token' \
  ./netspeedd

curl -H 'Authorization: Bearer separate-metrics-token' \
  http://127.0.0.1:8080/metrics
```

When `NETSPEEDD_METRICS_TOKEN` is empty, `/metrics` uses the access token. A
separate metrics token does not authorize the measurement API.

The Prometheus text endpoint reports:

- total and active HTTP requests;
- authentication rejections;
- active transfers and global/per-client transfer rejections;
- byte-quota rejections and measured download/upload bytes;
- admitted WebRTC offers, offer-rate rejections, capacity rejections, and active
  sessions;
- configured transfer and WebRTC ceilings;
- ICE/TURN configuration responses and rate rejections;
- rejected control-plane bodies and internal handler failures;
- embedded TURN accepted/read/written packets and bytes, dropped inbound bytes,
  and rejected outbound bytes.

Metrics are process-local and reset on restart.

## 9. configuration reference

| flag | environment variable | default / meaning |
|---|---|---|
| `-max-transfers` | `NETSPEEDD_MAX_CONCURRENT_TRANSFERS` | 256 global active transfers |
| `-max-client-transfers` | `NETSPEEDD_MAX_CONCURRENT_TRANSFERS_PER_CLIENT` | 24 active transfers per resolved client |
| `-client-quota-bytes` | `NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_BYTES` | 1 TiB per window; `0` disables |
| `-client-quota-window` | `NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_WINDOW` | 1 hour |
| `-max-webrtc-sessions` | `NETSPEEDD_MAX_WEBRTC_SESSIONS` | 64 global sessions |
| `-max-client-webrtc-sessions` | `NETSPEEDD_MAX_WEBRTC_SESSIONS_PER_CLIENT` | 2 sessions per client |
| `-webrtc-offers-per-minute` | `NETSPEEDD_WEBRTC_OFFER_RATE_PER_MINUTE` | 12; `0` disables |
| `-webrtc-offer-burst` | `NETSPEEDD_WEBRTC_OFFER_BURST` | 4 |
| `-turn-credentials-per-minute` | `NETSPEEDD_TURN_CREDENTIAL_RATE_PER_MINUTE` | 60; `0` disables |
| `-turn-credential-burst` | `NETSPEEDD_TURN_CREDENTIAL_BURST` | 10 |
| `-max-offer-body` | `NETSPEEDD_MAX_OFFER_BODY_BYTES` | 128 KiB |
| `-max-report-body` | `NETSPEEDD_MAX_REPORT_BODY_BYTES` | 16 KiB |
| `-access-token` | `NETSPEEDD_ACCESS_TOKEN` | optional shared bearer token |
| `-metrics` | `NETSPEEDD_ENABLE_METRICS` | false |
| `-metrics-token` | `NETSPEEDD_METRICS_TOKEN` | required for metrics unless access token is set |
| `-trusted-proxies` | `NETSPEEDD_TRUSTED_PROXY_CIDRS` | no trusted proxies |
| `-trust-proxy` | `NETSPEEDD_TRUST_PROXY` | legacy enable guard; CIDRs still required |
| `-turn-servers` | `NETSPEEDD_TURN_SERVERS` | comma-separated `stun:`, `stuns:`, `turn:`, or `turns:` URLs |
| `-turn-secret` | `NETSPEEDD_TURN_SECRET` | shared HMAC secret for TURN URLs |
| `-turn-realm` | `NETSPEEDD_TURN_REALM` | `netspeed` |
| none | `NETSPEEDD_MAX_TURN_TTL` | 600 seconds |
| `-embedded-turn` | `NETSPEEDD_EMBEDDED_TURN` | false |
| `-embedded-turn-addr` | `NETSPEEDD_EMBEDDED_TURN_ADDR` | `127.0.0.1:3478` |
| `-embedded-turn-ip` | `NETSPEEDD_EMBEDDED_TURN_PUBLIC_IP` | required for non-loopback listener |
| `-embedded-turn-max-mbps` | `NETSPEEDD_EMBEDDED_TURN_MAX_MBPS` | 100 Mbps combined UDP |

Existing daemon settings such as listen address, TLS paths, CORS origins, maximum
transfer bytes, locations, GeoIP databases, hostname, and colo remain available.
Run `netspeedd -h` for the flag list. The unsupported YAML example is absent;
flags and strictly parsed `NETSPEEDD_*` environment variables are the canonical
configuration surface. See [`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md).

## 10. remaining boundaries

These controls reduce unauthenticated bandwidth, memory, session, and relay
abuse, but they do not turn a shared-token speed test into a multi-tenant
identity system. Quotas are process-local: they do not persist across restarts
or coordinate across daemon replicas.

Endpoint-aware deadlines, cross-origin API routing and timing exposure,
response-writer recovery semantics, HTTP-first shutdown, TLS validation,
configuration, and independent ASN/City GeoIP wiring are canonical in
[`HTTP_DEPLOYMENT.md`](HTTP_DEPLOYMENT.md).

Release qualification includes genuine Pion WebRTC/TURN interoperability,
supported-OS CI, vulnerability scanning, end-to-end fixtures, and reproducible
release artifacts.
