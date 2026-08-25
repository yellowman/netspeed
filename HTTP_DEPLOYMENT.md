# Netspeed HTTP and deployment contract

This document is the canonical Phase 5 contract for HTTP deadlines, browser
routing, CORS, TLS, configuration, GeoIP, middleware behavior, and shutdown.
It supersedes older design examples in the combined daemon, CLI, and UI notes.

## 1. HTTP timeout model

`netspeedd` deliberately does not set `http.Server.ReadTimeout` or
`http.Server.WriteTimeout`. Whole-request deadlines make a valid slow upload or
download fail solely because it lasts longer than a fast request.

The server instead uses four independent limits:

| setting | default | scope |
|---|---:|---|
| `NETSPEEDD_READ_HEADER_TIMEOUT` / `-read-header-timeout` | 10s | request headers only |
| `NETSPEEDD_CONTROL_TIMEOUT` / `-control-timeout` | 30s | metadata, health, locations, metrics, signaling, reports, and static files |
| `NETSPEEDD_TRANSFER_TIMEOUT` / `-transfer-timeout` | 5m | one `/__down` or `/__up` request |
| `NETSPEEDD_IDLE_TIMEOUT` / `-idle-timeout` | 2m | an idle keep-alive connection |

Endpoint wrappers set connection read/write deadlines only for the operation
that needs them and clear those deadlines before the connection can be reused.
They also attach the same deadline to the request context. Download requests get
a write deadline; uploads get read and write deadlines; control requests get
both. The transfer timeout must be large enough for the largest permitted
request at the slowest link rate the deployment intends to support.

## 2. Browser routing

### Same-origin deployment

When the UI and daemon share an origin, no browser configuration is required.
The browser uses `/meta`, `/__down`, `/__up`, `/locations`, and `/api/*` on the
page origin.

The simplest configuration is:

```bash
./netspeedd -web-dir ./web
```

A reverse proxy can also serve the static UI and proxy the daemon routes on the
same origin.

### Separate UI and API origins

Define `globalThis.NETSPEED_CONFIG` before loading `speedtest.js`:

```html
<script>
  globalThis.NETSPEED_CONFIG = {
    apiBaseUrl: "https://speed-api.example.com/",
    credentials: "omit",
    accessToken: "optional-deployment-token"
  };
</script>
<script src="js/speedtest.js"></script>
```

`apiBaseUrl` may include a path prefix. A trailing slash is added when absent.
For example, `https://example.com/netspeed-api` resolves `/meta` to
`https://example.com/netspeed-api/meta`. The value must use HTTP or HTTPS and
must not contain URL credentials, a query, or a fragment.

`credentials` accepts the Fetch values `omit`, `same-origin`, or `include` and
defaults to `same-origin`. The same policy is used for metadata, throughput,
latency, upload fallback, TURN credentials, packet-test signaling, and packet
reports. Because XHR cannot suppress same-origin cookies, a non-streaming
browser configured with same-origin `omit` uses Fetch for that upload and marks
loaded-upload timing imprecise rather than violating the credential policy.
`include` should be used only when the deployment actually uses cookies or HTTP
authentication and the daemon is configured for credentialed CORS.

A bearer token configured as `accessToken` is sent in the `Authorization`
header. A token embedded in browser JavaScript is visible to that browser and
is not a secret from the user.

## 3. CORS and Resource Timing

CORS is enabled by default with wildcard, non-credentialed access. Configure
explicit browser origins for production:

```bash
NETSPEEDD_ALLOWED_ORIGINS='https://speed-ui.example.com' \
NETSPEEDD_CORS_ALLOW_CREDENTIALS=false \
  ./netspeedd
```

Multiple origins are comma-separated. Origins must contain only scheme and
authority. Wildcard origin is rejected when credentialed CORS is enabled:

```bash
NETSPEEDD_ALLOWED_ORIGINS='https://speed-ui.example.com' \
NETSPEEDD_CORS_ALLOW_CREDENTIALS=true \
  ./netspeedd
```

For an allowed origin the daemon returns:

- `Access-Control-Allow-Origin`;
- `Access-Control-Allow-Credentials: true` when enabled;
- `Timing-Allow-Origin` with the same origin decision;
- `Access-Control-Expose-Headers` for measurement, quota, timing, and metadata
  headers;
- explicit preflight methods and headers.

Both preflight and actual requests carrying a disallowed `Origin` receive
`403 Forbidden`. Requests without an `Origin` header, including the Go CLI,
remain unaffected.

`Timing-Allow-Origin` is required for a separately hosted browser to inspect
cross-origin Resource Timing fields. Without it, the browser falls back to less
precise application timestamps and marks timing confidence accordingly.

## 4. Reverse proxying

When a reverse proxy terminates TLS, bind the daemon to a private or loopback
address and list every trusted proxy network explicitly:

```bash
./netspeedd \
  -listen 127.0.0.1:8080 \
  -trusted-proxies '127.0.0.0/8,10.0.0.0/8'
```

Forwarding headers from all other peers are ignored. The proxy must not buffer,
compress, cache, or transform `/__down` or `/__up`; doing so changes the path
being measured. It should also permit request and response bodies up to
`NETSPEEDD_MAX_BYTES` and use upstream timeouts at least as long as
`NETSPEEDD_TRANSFER_TIMEOUT`.

A path-prefixed deployment can strip the prefix before proxying. For example,
an upstream route `/netspeed-api/` should present `/meta`, `/__down`, and the
other root paths to `netspeedd`, while the browser uses
`apiBaseUrl: "https://example.com/netspeed-api/"`.

## 5. Direct TLS

Direct TLS requires both files:

```bash
./netspeedd \
  -listen :443 \
  -tls-cert /etc/netspeed/fullchain.pem \
  -tls-key /etc/netspeed/private-key.pem
```

Certificate-only and key-only configurations are rejected. The certificate/key
pair is loaded before the listener starts; unreadable, malformed, or mismatched
files cause startup failure instead of silent cleartext fallback. Direct TLS
requires TLS 1.2 or newer.

## 6. Configuration surface

The daemon supports environment variables and command-line flags. Flags
explicitly supplied on the command line override environment values. Invalid
booleans, integers, durations, CIDRs, origins, TLS combinations, and other
unsafe combinations cause startup failure.

There is no YAML loader and no `--config` flag. The unsupported YAML example was
removed in Phase 5. A complete shell-compatible example is provided at:

```text
configs/netspeedd.env.example
```

Typical use:

```bash
set -a
. /etc/netspeedd.env
set +a
./netspeedd
```

Run `netspeedd -h` for the flag surface. The environment example is the compact
reference for all `NETSPEEDD_*` settings.

## 7. GeoIP

ASN and City databases are independent:

```bash
NETSPEEDD_GEOIP_ASN_DB=/var/lib/GeoIP/GeoLite2-ASN.mmdb \
NETSPEEDD_GEOIP_CITY_DB=/var/lib/GeoIP/GeoLite2-City.mmdb \
  ./netspeedd
```

The ASN database supplies autonomous-system number and organization. The City
database supplies country, subdivision, city, postal code, coordinates, and
timezone. Either may be configured alone. The Phase 4
`NETSPEEDD_GEOIP_DB` name remains a compatibility alias for the ASN database.

A configured database that cannot be opened is a startup error. With no City
database, location fields remain unknown; the daemon no longer labels every
unknown client as being in the United States.

## 8. Response middleware contract

The logging writer records the first committed status and actual body bytes.
Subsequent `WriteHeader` calls cannot rewrite the status. It supports
`Unwrap` and delegates streaming, flushing, hijacking, HTTP/2 push,
`io.ReaderFrom`, connection deadlines, and full-duplex control to the underlying
writer when supported.

Recovery returns `500 Internal Server Error` only before a response is
committed. A panic after headers or body bytes have been sent aborts the stream
with `http.ErrAbortHandler`; appending a replacement error body would corrupt a
measurement response.

## 9. Shutdown ownership

Graceful shutdown follows this order:

1. stop accepting new HTTP work;
2. wait for active HTTP handlers to drain;
3. shut down WebRTC sessions;
4. close GeoIP readers;
5. close the embedded TURN server after the HTTP drain succeeds.

If the shutdown context expires, shared dependencies remain open so active
handlers do not observe closed resources. The caller may retry graceful
shutdown with another context. Forced `Close` is reserved for listener/startup
failure paths or process termination.

## 10. Deployment checklist

- Use same-origin routing or configure `apiBaseUrl` and matching CORS origins.
- Do not enable credentialed CORS with wildcard origins.
- Keep measurement routes unbuffered, uncompressed, uncached, and
  untransformed at the proxy.
- Set proxy body limits and timeouts at or above the daemon limits.
- Configure trusted proxy CIDRs before relying on forwarded client identity.
- Configure both TLS files or neither.
- Treat configured GeoIP database failures as deployment failures.
- Use the Phase 4 admission, quota, authentication, and TURN controls described
  in [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md).
