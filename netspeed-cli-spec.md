# netspeed CLI - command line speed test client spec

this document specifies the **command-line client behavior** shared by the Go and native C speed test CLIs
that talks to a backend exposing these endpoints:

- `GET /meta`
- `GET /__down`
- `POST /__up`
- `GET /__ws` (advertised WebSocket upgrade)
- `GET` or `HEAD /__ping`
- `GET /locations`
- `GET /api/turn/credentials`
- `POST /api/packet-test/offer`
- `POST /api/packet-test/report`

this is a **CLI-only** spec: it defines what requests the client makes,
what responses it expects, how tests are orchestrated, and how results are
displayed in the terminal. backend implementation details (TURN server config, HMAC secrets,
etc.) are intentionally out of scope.

---


> **Implemented authority:** the Go CLI requires measurement protocol version 2.
> [`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md) is the canonical
> measurement contract, and [`SERVICE_HARDENING.md`](SERVICE_HARDENING.md)
> defines bearer authentication and server admission responses. The
> configuration-file sections below are legacy design notes. The Go and C
> clients are both protocol-v2 implementations; implementation-specific build
> and qualification details are in [`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md).

## 1. command line interface

### 1.1 basic usage

```
netspeed [flags] [server-url]
```

**positional arguments:**

- `server-url` - base URL of the speed test server (default: `http://localhost:8080`)

**examples:**

```bash
# Run full test against default server
netspeed

# Run test against specific server
netspeed https://speed.example.com

# Quick test (fewer fixed windows and latency samples)
netspeed --quick

# JSON output for scripting
netspeed --json

# Verbose output with timing details
netspeed -v
```

### 1.2 command line flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--provider` | | `auto` | `auto`, `netspeed`, or `cloudflare` |
| `--server` | `-s` | `http://localhost:8080` | Server URL |
| `--token` | | `NETSPEED_TOKEN` | Shared bearer token for protected servers |
| `--quick` | `-q` | false | Quick test mode (fewer samples) |
| `--download-only` | `-d` | false | Skip upload tests |
| `--upload-only` | `-u` | false | Skip download tests |
| `--no-packet-loss` | | false | Skip packet loss test |
| `--download-payload` | | `auto` | Netspeed negotiated value or native Cloudflare observed-default constraint: `auto`, `random`, or `zero` |
| `--download-framing` | | `auto` | Netspeed negotiated value or native Cloudflare observed-default constraint: `auto`, `fixed`, or `chunked` |
| `--download-chunk-bytes` | | `0` | Application chunk size; Cloudflare requires exact response-header evidence |
| `--download-flush` | | `auto` | Per-chunk flush; Cloudflare requires exact response-header evidence |
| `--json` | `-j` | false | Output results as JSON |
| `--csv` | | false | Output results as CSV |
| `--verbose` | `-v` | false | Show detailed progress |
| `--quiet` | | false | Minimal output (final results only) |
| `--timeout` | `-t` | 60s | Total test timeout |
| `--no-color` | | false | Disable colored output |
| `--version` | `-V` | | Show version and exit |
| `--help` | `-h` | | Show help and exit |

### 1.3 exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Test failed (network error, timeout) |
| 2 | Invalid arguments |
| 3 | Server unreachable |
| 4 | Configuration error |

---

## 2. backend API surface (from CLI's point of view)

### 2.1 HTTP measurement endpoints

#### 2.1.1 `GET /meta` - client / server metadata

**request**

- method: `GET`
- path: `/meta`
- query params: none
- body: none

**response (200)**

```json
{
  "hostname": "speed.example.com",
  "clientIp": "203.0.113.42",
  "httpProtocol": "HTTP/2.0",
  "asn": 13254,
  "asOrganization": "Example ISP, Inc.",
  "colo": "PDX",
  "country": "US",
  "city": "Bend",
  "region": "Oregon",
  "postalCode": "97701",
  "latitude": 44.0582,
  "longitude": -121.3153,
  "timezone": "America/Los_Angeles",
  "maxTransferBytes": 1073741824,
  "maxConcurrentTransfersPerClient": 24,
  "measurementProtocolVersion": 2,
  "uploadReceiptVersion": 1,
  "packetLossFrameVersion": 1,
  "measurementCapabilities": {
    "version": 1,
    "downloadPath": "/__down",
    "uploadPath": "/__up",
    "httpPingPath": "/__ping",
    "httpPingMethods": ["GET", "HEAD"],
    "webSocketPingPath": "/__ws",
    "webSocketPingProtocol": "netspeed.ping.v1",
    "webSocketPingPayloadBytes": 16,
    "downloadPayloads": ["random", "zero"],
    "downloadFramings": ["fixed", "chunked"]
  }
}
```

**CLI usage**

- send `Authorization: Bearer <token>` on every service request when `--token`
  or `NETSPEED_TOKEN` is set;
- cap all request batches at `maxConcurrentTransfersPerClient` and cap sustained
  load flows at one less than that value so a loaded-latency probe has a slot;
- fail capability negotiation when the advertised per-client ceiling is below 2;
- validate transport endpoint paths and query names before using them;
- reject explicit transport controls when version 1 capabilities are absent or
  do not support the requested value;
- expose the normalized choice in JSON as `meta.measurementSelection`;
- display server and client info in header:
  ```
  Server: speed.example.com (PDX)
  Client: 203.0.113.42 (Example ISP, Inc. AS13254)
  ```

Overload and authentication responses are not measurements. The CLI
rejects `401`, `429`, and `503` responses and does not time their bodies as
throughput or latency samples.

---

#### 2.1.2 `GET /__down` - download / latency

**request**

- method: `GET`
- path: `/__down`
- query parameters:
  - `bytes` - exact response-body length; `0` is a latency probe;
  - `measId` - unique opaque correlation id;
  - `profile` and `run` - diagnostic labels;
  - `during` - `download`, `upload`, or omitted for unloaded probes;
  - advertised transport-version-1 names for payload (`random|zero`), framing
    (`fixed|chunked`), application chunk bytes, and per-chunk flush behavior.

The strict Go and native C clients use the names from
`measurementCapabilities`; they do not assume that a future daemon retains the
literal keys shown above. Explicit controls are never sent to a legacy server.

The Go and native C Cloudflare adapters do not send those optional discriminator
parameters. They first fetch 64 KiB through the common `bytes` key, classify the
provider-default payload and framing, and treat explicit transport flags as
requirements on that observed behavior. Chunk size and flush settings require
exact response-header evidence. A mismatch is an argument error rather than a
silently ignored flag. Later downloads must retain the probed payload and
framing classification.

**accepted response**

- status `200`;
- `Content-Type: application/octet-stream`;
- `Content-Length`, when present, equals the requested byte count;
- body contains exactly the requested number of bytes;
- negotiated responses carry matching measurement, payload, framing, and chunk
  diagnostic headers plus `Cache-Control: no-store, no-transform`;
- a streamed response has no `Content-Length`, while fixed framing has the exact
  requested length.

For throughput, the native clients consume the body without retaining it and
time first response byte through completed body read. For latency, each client
uses its transport's request-complete-to-first-byte interval. Any status, type,
length, read, or timing mismatch invalidates the sample.

A daemon advertising the exact `/__ws`, `netspeed.ping.v1`, and 16-byte payload
contract is first probed through one persistent application-level binary echo.
The clients send one unreported warmup, then measure only message send to exact
nonce echo; DNS, TCP, TLS, HTTP Upgrade, and warmup are excluded. Any upgrade,
timeout, close, framing, subprotocol, or echo failure disables WebSocket for the
remainder of the run.

HTTP fallback uses the advertised `httpPingPath` with its preferred supported
`GET` or `HEAD` method and a zero-byte response. When the server advertises
`warmConnectionPing`, strict Go and native C clients observe connection reuse,
discard cold attempts, and report only a reused keep-alive sample. The final
compatibility fallback is `GET /__down?bytes=0`. JSON latency samples include
`connectionReused`, `probeTransport`, `probeMethod`, `probePath`, optional
`webSocketProtocol`, and the stable `probeFallbackReason` on HTTP samples after
a WebSocket failure.

The Go and native C Cloudflare adapters always use that fallback on a dedicated
transport limited to one connection. They prime the connection for each idle or
loaded condition and accept only probes with observed connection reuse; up to
four cold attempts are discarded rather than reported. Their JSON results add
warm-sample, warmup, discarded-cold, server-timing-adjustment, and observed HTTP
protocol evidence.

#### 2.1.3 `POST /__up` - verified upload

**request**

- method: `POST`
- path: `/__up`
- query parameters: the advertised exact byte-count key, unique `measId`, and
  diagnostic `profile` and `run` labels;
- `Content-Type: application/octet-stream`;
- `Content-Encoding: identity`;
- exact `Content-Length`;
- a bounded streaming body, never a retained profile-sized allocation.

**accepted response**

```json
{
  "ok": true,
  "acceptedBytes": 1000000,
  "serverDurationNs": 123456789
}
```

The client requires status `200`, JSON content type, receipt version 1,
anti-transform response controls, matching upload diagnostic headers, and
`acceptedBytes` equal to the declared and actually generated body length. The
server body-read duration is canonical; client request-body timing is a fallback.
A truncated, oversized, rejected, or unverifiable upload is not a sample.

#### 2.1.4 `GET /locations` - test locations

**request**

- method: `GET`
- path: `/locations`

**response (200)**

```json
[
  {
    "iata": "PDX",
    "lat": 45.5898,
    "lon": -122.5951,
    "cca2": "US",
    "region": "North America",
    "city": "Portland"
  }
]
```

**CLI usage**

- match `meta.colo` to find active server location.
- display in verbose mode: `Server location: Portland, US (PDX)`

---

### 2.2 TURN & WebRTC endpoints

#### 2.2.1 `GET /api/turn/credentials`

**purpose:** obtain temporary TURN credentials for WebRTC packet loss test.

**response (200)**

```json
{
  "username": "1701532800:abcd1234",
  "credential": "opaque-password-string",
  "ttlSec": 600,
  "servers": [
    "stun:turn1.example.com:3478",
    "turn:turn1.example.com:3478?transport=udp"
  ]
}
```

**CLI usage**

- configure pion/webrtc ICE servers with credentials.

---

#### 2.2.2 `POST /api/packet-test/offer`

**purpose:** send WebRTC offer, receive answer and test ID.

**request body:**

```json
{
  "sdp": "<offer-sdp>",
  "type": "offer",
  "testProfile": "loss-exact-v1"
}
```

**response (200)**

```json
{
  "sdp": "<answer-sdp>",
  "type": "answer",
  "testId": "c65b0b1d-6f7f-4a9a-9f2b-7c9d3c5f0c3a"
}
```

---

## 3. measurement pipeline

The CLI runs:

1. `GET /meta` and require measurement protocol 2;
2. unloaded latency probes;
3. download baselines followed by sustained download windows, with loaded
   latency inside the selected window;
4. upload baselines followed by sustained upload windows, with loaded latency
   inside the selected window;
5. the exact-frame WebRTC packet test unless disabled;
6. shared summaries, grades, and confidence gates.

### 3.1 verified baselines and fixed windows

Each enabled direction runs three verified 100 kB requests and three verified
1 MB requests. At least two requests in each group must succeed. The median 1 MB
result chooses a bounded chunk and flow count; baselines do not influence the
headline speed once window samples exist.

Normal mode runs three 1.5-second windows. Quick mode runs one 1-second window.
Workers repeatedly complete exact, independently verified requests. Aggregate
window speed is verified bytes divided by elapsed wall-clock time. Request chunks
are rounded to 64 KiB, never exceed 256 MiB or `maxTransferBytes`, and high rates
scale through 1, 2, 4, 8, or 16 flows rather than giant requests.

### 3.2 precise timing

The CLI uses `net/http/httptrace`:

- latency RTT: `GotFirstResponseByte - WroteRequest`;
- download request body: first response byte through exact body completion;
- upload request lifecycle: first body read through verified receipt completion;
- accepted upload throughput: daemon `serverDurationNs`, with client body timing
  as a diagnostic fallback;
- aggregate window: worker-release time through completion of all in-flight
  verified requests after the stop signal.

Connection setup is not classified as transfer load. Receipt completion remains
inside the active upload request for overlap tracking; throughput still uses the
daemon body-read duration.

### 3.3 continuous loaded-latency proof

A shared load owner tracks active transfer bodies and increments a generation
every time the active count reaches zero. A loaded probe is retained only when
load is active before and after the probe and the generation is unchanged. A
probe spanning any gap is rejected and retried.

Normal mode targets five probes in the middle window and requires at least three
accepted probes per enabled direction. Quick mode targets three in its single
window. Download load means response-body consumption; upload load begins with
request-body transmission and ends after the verified receipt. The window timer
stops new transfers and further probe retries. Requests already in flight drain into the window aggregate. A timer
expiry is successful only when the accepted probe quorum has already been met;
otherwise that direction fails rather than allowing retries to extend the test.

### 3.4 exact-size packet test

The CLI creates an unordered `maxRetransmits: 0` data channel and sends exact
1,200-byte binary frames defined in `MEASUREMENT_PROTOCOL_V2.md`. Sequence
numbers advance only for successful local sends. The daemon acknowledges each
unique valid probe once and returns authoritative counters from
`POST /api/packet-test/report`.

The result reports separately:

- round-trip transaction loss;
- server-observed forward probe loss;
- reverse acknowledgement loss.

Unavailable packet testing remains unavailable and never becomes numeric zero.

### 3.5 shared statistics

The Go and browser clients both use R-7 percentile interpolation, conservative
1.5-IQR filtering, p90-minus-median jitter, and population coefficient of
variation. Headline throughput is the R-7 p90 of fixed-window values; latency is
the median after configured warmup removal and filtering.

## 4. CLI data model

```go
type LatencyCondition string
const (
    ConditionUnloaded LatencyCondition = "unloaded"
    ConditionDownload LatencyCondition = "download"
    ConditionUpload   LatencyCondition = "upload"
)

type LatencySample struct {
    Timestamp           time.Time
    RTT                 time.Duration
    Condition           LatencyCondition
    ConnectionReused    bool
    ProbeTransport      string // websocket or http
    ProbeMethod         string // MESSAGE, GET, or HEAD
    ProbePath           string
    ProbeFallbackReason string
    WebSocketProtocol   string
}

type ThroughputDirection string
const (
    DirectionDownload ThroughputDirection = "download"
    DirectionUpload   ThroughputDirection = "upload"
)

type ThroughputSample struct {
    Timestamp  time.Time
    Direction  ThroughputDirection
    SizeBytes  int64
    Duration   time.Duration
    Mbps       float64
    Profile    string
    RunIndex   int
}

type RTTStats struct {
    Min    float64 `json:"min"`
    Median float64 `json:"median"`
    P90    float64 `json:"p90"`
}

type PacketLossResult struct {
    Sent        int      `json:"sent"`
    Received    int      `json:"received"`
    LossPercent float64  `json:"lossPercent"`
    RTTStatsMs  RTTStats `json:"rttStatsMs"`
    JitterMs    float64  `json:"jitterMs"`
    TestID      string   `json:"testId,omitempty"`
    Unavailable bool     `json:"unavailable,omitempty"`
    Reason      string   `json:"reason,omitempty"`
}

type Summary struct {
    DownloadMbps      float64 `json:"downloadMbps"`
    UploadMbps        float64 `json:"uploadMbps"`
    LatencyUnloadedMs float64 `json:"latencyUnloadedMs"`
    LatencyDownloadMs float64 `json:"latencyDownloadMs"`
    LatencyUploadMs   float64 `json:"latencyUploadMs"`
    JitterMs          float64 `json:"jitterMs"`
    PacketLossPercent float64 `json:"packetLossPercent"`
}

type Meta struct {
    Hostname       string  `json:"hostname"`
    ClientIP       string  `json:"clientIp"`
    HTTPProtocol   string  `json:"httpProtocol"`
    ASN            int     `json:"asn"`
    ASOrganization string  `json:"asOrganization"`
    Colo           string  `json:"colo"`
    Country        string  `json:"country"`
    City           string  `json:"city"`
    Region         string  `json:"region"`
    PostalCode     string  `json:"postalCode"`
    Latitude       float64 `json:"latitude"`
    Longitude      float64 `json:"longitude"`
    Timezone       string  `json:"timezone,omitempty"`
}
```

---

## 5. terminal output

### 5.1 ASCII spinners

use simple ASCII character sequences for progress indication:

```go
var spinnerFrames = []string{"|", "/", "-", "\\"}
var dotSpinner = []string{".", "..", "...", ""}
var blockSpinner = []string{"[    ]", "[=   ]", "[==  ]", "[=== ]", "[====]", "[ ===]", "[  ==]", "[   =]"}
```

**spinner usage:**

```go
type Spinner struct {
    frames   []string
    current  int
    interval time.Duration
    stop     chan struct{}
}

func (s *Spinner) Start(prefix string) {
    go func() {
        for {
            select {
            case <-s.stop:
                return
            default:
                fmt.Printf("\r%s %s", prefix, s.frames[s.current])
                s.current = (s.current + 1) % len(s.frames)
                time.Sleep(s.interval)
            }
        }
    }()
}

func (s *Spinner) Stop(finalText string) {
    close(s.stop)
    fmt.Printf("\r%s\n", finalText)
}
```

---

### 5.2 progress bars

ASCII progress bars for long-running operations:

```go
func ProgressBar(current, total int, width int) string {
    percent := float64(current) / float64(total)
    filled := int(percent * float64(width))
    empty := width - filled

    bar := strings.Repeat("=", filled)
    if filled < width {
        bar += ">"
        empty--
    }
    bar += strings.Repeat(" ", empty)

    return fmt.Sprintf("[%s] %3.0f%%", bar, percent*100)
}
```

**output example:**

```
Download: [=========>          ]  45%  523.4 Mbps
```

---

### 5.3 standard output format

**header:**

```
netspeed v1.0.0
Server: speed.example.com (PDX - Portland, US)
Client: 203.0.113.42 (Example ISP AS13254)
Protocol: HTTP/2.0
────────────────────────────────────────────────
```

**progress (interactive mode):**

```
Testing latency... [===>    ] 15/20
Testing download... [=======>] 78% 756.2 Mbps
Testing upload... |
Testing packet loss... [====] 834/1000 sent
```

**results:**

```
────────────────────────────────────────────────
                    RESULTS
────────────────────────────────────────────────
  Download:     892.4 Mbps
  Upload:       634.2 Mbps
  Latency:        6.2 ms (jitter: 1.4 ms)
  Packet Loss:    0.20% (998/1000)
────────────────────────────────────────────────
  Network Quality: Great
    Video Streaming:  Great
    Online Gaming:    Great
    Video Chatting:   Great
────────────────────────────────────────────────
```

---

### 5.4 verbose output format

with `-v` flag, show detailed breakdown:

```
────────────────────────────────────────────────
LATENCY BREAKDOWN
────────────────────────────────────────────────
  Unloaded:   6.2 ms (min: 5.1, max: 8.4, p90: 7.8)
  Download:  14.3 ms (min: 11.2, max: 21.6, p90: 18.4)
  Upload:    18.7 ms (min: 14.1, max: 28.3, p90: 24.1)

────────────────────────────────────────────────
DOWNLOAD TESTS (31 samples)
────────────────────────────────────────────────
  100kB x10:  avg 234.5 Mbps  (min: 189, max: 312)
  1MB x8:     avg 678.2 Mbps  (min: 542, max: 812)
  10MB x6:    avg 856.3 Mbps  (min: 798, max: 923)
  25MB x4:    avg 891.2 Mbps  (min: 867, max: 912)
  100MB x3:   avg 892.4 Mbps  (min: 888, max: 901)

────────────────────────────────────────────────
UPLOAD TESTS (25 samples)
────────────────────────────────────────────────
  100kB x8:   avg 198.3 Mbps  (min: 156, max: 278)
  1MB x6:     avg 512.4 Mbps  (min: 423, max: 598)
  10MB x4:    avg 612.8 Mbps  (min: 567, max: 654)
  25MB x4:    avg 632.1 Mbps  (min: 598, max: 678)
  50MB x3:    avg 634.2 Mbps  (min: 621, max: 651)

────────────────────────────────────────────────
PACKET LOSS TEST
────────────────────────────────────────────────
  Sent:       1000 packets
  Received:    998 packets
  Loss:        0.20%
  RTT:        min 12.3 ms, median 15.6 ms, p90 21.2 ms
  Jitter:      3.4 ms
  Connection:  TURN relay (UDP)
  Pattern:     Random
```

---

### 5.5 quiet output format

with `--quiet` flag, minimal output:

```
892.4  634.2  6.2  0.20
```

format: `download_mbps upload_mbps latency_ms loss_percent`

---

### 5.6 JSON output format

with `--json` flag, output matches the web client format exactly:

```json
{
  "meta": {
    "hostname": "speed.example.com",
    "clientIp": "203.0.113.42",
    "httpProtocol": "HTTP/2.0",
    "asn": 13254,
    "asOrganization": "Example ISP, Inc.",
    "colo": "PDX",
    "country": "US",
    "city": "Bend",
    "region": "Oregon",
    "postalCode": "97701",
    "latitude": 44.0582,
    "longitude": -121.3153,
    "timezone": "America/Los_Angeles"
  },
  "summary": {
    "downloadMbps": 892.4,
    "uploadMbps": 634.2,
    "latencyUnloadedMs": 6.2,
    "latencyDownloadMs": 14.3,
    "latencyUploadMs": 18.7,
    "jitterMs": 1.4,
    "packetLossPercent": 0.2
  },
  "quality": {
    "videoStreaming": "Great",
    "gaming": "Great",
    "videoChatting": "Great"
  },
  "throughputSamples": [
    {
      "ts": 1705315425123,
      "direction": "download",
      "sizeBytes": 100000,
      "durationMs": 12.5,
      "mbps": 64.0,
      "profile": "100kB",
      "runIndex": 0
    }
  ],
  "latencySamples": [
    {
      "ts": 1705315420456,
      "rttMs": 6.2,
      "condition": "unloaded",
      "connectionReused": true,
      "probeTransport": "websocket",
      "probeMethod": "MESSAGE",
      "probePath": "/__ws",
      "webSocketProtocol": "netspeed.ping.v1"
    }
  ],
  "packetLoss": {
    "sent": 1000,
    "received": 998,
    "lossPercent": 0.2,
    "rttStatsMs": {
      "min": 12.3,
      "median": 15.6,
      "p90": 21.2
    },
    "jitterMs": 3.4,
    "testId": "c65b0b1d-6f7f-4a9a-9f2b-7c9d3c5f0c3a"
  },
  "startTime": "2024-01-15T10:23:40.123456789Z",
  "endTime": "2024-01-15T10:24:25.987654321Z"
}
```

Cloudflare provider JSON is a separate compatibility result rather than the
strict web-result schema. The Go and native C `cloudflare-http-v2` objects
include an
`httpTransport` section with behavioral-probe selection evidence and an
`antiTransform` section. Its idle and loaded latency objects include
`connectionReused`, `warmSamples`, `warmupRequests`,
`discardedColdAttempts`, `serverTimingAdjustedSamples`, `probeTransport`,
`probeMethod`, `probePath`, and `httpProtocols`.

**when packet loss test is unavailable:**

```json
{
  "packetLoss": {
    "sent": 0,
    "received": 0,
    "lossPercent": 0,
    "rttStatsMs": {
      "min": 0,
      "median": 0,
      "p90": 0
    },
    "jitterMs": 0,
    "unavailable": true,
    "reason": "WebRTC packet loss test not yet implemented in CLI"
  }
}
```

---

### 5.7 CSV output format

with `--csv` flag:

```csv
timestamp,server,download_mbps,upload_mbps,latency_ms,jitter_ms,packet_loss_pct
2024-01-15T10:23:45Z,speed.example.com,892.4,634.2,6.2,1.4,0.20
```

---

### 5.8 color scheme

**ANSI color codes (when `--no-color` is not set):**

```go
const (
    ColorReset  = "\033[0m"
    ColorRed    = "\033[31m"
    ColorGreen  = "\033[32m"
    ColorYellow = "\033[33m"
    ColorBlue   = "\033[34m"
    ColorCyan   = "\033[36m"
    ColorBold   = "\033[1m"
    ColorDim    = "\033[2m"
)
```

**usage:**

| Element | Color |
|---------|-------|
| Headers/Labels | Bold |
| Good values (Great) | Green |
| Okay values (Good/Okay) | Yellow |
| Poor values (Poor) | Red |
| Speed values | Cyan |
| Latency values | Blue |
| Progress bars | Default |
| Spinners | Dim |

---

## 6. error handling

### 6.1 network errors

```go
type TestError struct {
    Operation string // "meta", "download", "upload", "latency", "packet-loss"
    Message   string
    Err       error
}

func (e *TestError) Error() string {
    return fmt.Sprintf("%s: %s", e.Operation, e.Message)
}
```

**error output:**

```
Error: Failed to connect to server
  Server: speed.example.com
  Reason: connection refused

Retry with: netspeed -s https://speed.example.com
```

---

### 6.2 packet loss test errors

| Error | CLI Display |
|-------|-------------|
| ICE connection timeout | `Packet Loss: N/A (ICE timeout)` |
| ICE connection failed | `Packet Loss: N/A (connection failed)` |
| TURN not configured | `Packet Loss: N/A (TURN unavailable)` |
| Data channel error | `Packet Loss: N/A (channel error)` |

**N/A display in results:**

```
────────────────────────────────────────────────
  Download:     892.4 Mbps
  Upload:       634.2 Mbps
  Latency:        6.2 ms (jitter: 1.4 ms)
  Packet Loss:  N/A (ICE connection timeout)
────────────────────────────────────────────────
```

---

### 6.3 partial results

if some tests fail, still display available results:

```
────────────────────────────────────────────────
                    RESULTS
────────────────────────────────────────────────
  Download:     892.4 Mbps
  Upload:       N/A (test failed)
  Latency:        6.2 ms (jitter: 1.4 ms)
  Packet Loss:    0.20%
────────────────────────────────────────────────
  Network Quality: Unknown (incomplete test)
────────────────────────────────────────────────
```

---

## 7. bounded window selection

The CLI no longer selects increasingly large profiles. It estimates a per-flow
request intended to last about 250 ms:

```text
target bytes = estimated bits/s / 8 × 0.250 s / concurrency
```

The chunk is rounded up to 64 KiB and bounded to 100,000 bytes through 256 MiB,
then capped by the server's `maxTransferBytes`. Flow count is selected from the
baseline estimate:

| estimate | flows |
|---:|---:|
| below 100 Mbps | 1 |
| 100–499 Mbps | 2 |
| 500–1,999 Mbps | 4 |
| 2–9.999 Gbps | 8 |
| 10 Gbps or more | 16 |

The selected count is capped at `maxConcurrentTransfersPerClient - 1`; the
remaining slot is reserved for a loaded-latency probe. Each flow reuses the bounded generator and issues complete requests until the
window owner stops it. No profile-sized payload is cached.

## 8. network quality scoring

### 8.1 quality grades

```go
type Grade string
const (
    GradeGreat Grade = "Great"
    GradeGood  Grade = "Good"
    GradeOkay  Grade = "Okay"
    GradePoor  Grade = "Poor"
)

type NetworkQuality struct {
    VideoStreaming string `json:"videoStreaming"`
    Gaming         string `json:"gaming"`
    VideoChatting  string `json:"videoChatting"`
}
```

### 8.2 grading algorithm

```go
func gradeForStreaming(s Summary) Grade {
    if s.DownloadMbps >= 50 && s.LatencyUnloaded <= 25*time.Millisecond &&
       s.Jitter <= 5*time.Millisecond && s.PacketLossPercent <= 0.5 {
        return GradeGreat
    }
    if s.DownloadMbps >= 20 && s.LatencyUnloaded <= 50*time.Millisecond &&
       s.Jitter <= 15*time.Millisecond && s.PacketLossPercent <= 1.5 {
        return GradeGood
    }
    if s.DownloadMbps >= 10 && s.LatencyUnloaded <= 80*time.Millisecond &&
       s.Jitter <= 30*time.Millisecond && s.PacketLossPercent <= 3 {
        return GradeOkay
    }
    return GradePoor
}

func gradeForGaming(s Summary) Grade {
    // Stricter latency requirements
    if s.DownloadMbps >= 25 && s.LatencyUnloaded <= 20*time.Millisecond &&
       s.Jitter <= 5*time.Millisecond && s.PacketLossPercent <= 0.1 {
        return GradeGreat
    }
    // ...
}

func gradeForVideoChat(s Summary) Grade {
    // Balanced upload/download, stricter jitter
    if s.DownloadMbps >= 10 && s.UploadMbps >= 5 &&
       s.LatencyUnloaded <= 50*time.Millisecond &&
       s.Jitter <= 10*time.Millisecond && s.PacketLossPercent <= 1 {
        return GradeGreat
    }
    // ...
}
```

---

## 9. configuration contract

The Go CLI intentionally has no YAML loader and no `--config` flag. Server URL,
mode, output, timeout, and operation-selection behavior are controlled by the
command-line flags in section 1. The bearer token may also be supplied through
`NETSPEED_TOKEN`; an explicit `--token` takes precedence.

This decision avoids a second partially implemented configuration grammar. The
daemon independently uses flags plus strictly parsed `NETSPEEDD_*` environment
variables as documented in `HTTP_DEPLOYMENT.md` and
`configs/netspeedd.env.example`.

---

## 10. implementation notes

### 10.1 HTTP client configuration

the client must use optimized TCP settings for high-speed transfers:

**buffer sizes:**
```go
const (
    ReadBufferSize  = 4 * 1024 * 1024 // 4MB read buffer
    WriteBufferSize = 4 * 1024 * 1024 // 4MB write buffer
)
```

**TCP optimizations:**
- `SetNoDelay(true)` - disable Nagle's algorithm for lower latency
- `SetReadBuffer(4MB)` - large read buffer for high-speed downloads
- `SetWriteBuffer(4MB)` - large write buffer for high-speed uploads

**full configuration:**

```go
func newHTTPClient() *http.Client {
    dialer := &net.Dialer{
        Timeout:   30 * time.Second,
        KeepAlive: 30 * time.Second,
    }

    return &http.Client{
        Transport: &http.Transport{
            DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
                conn, err := dialer.DialContext(ctx, network, addr)
                if err != nil {
                    return nil, err
                }

                // Set TCP options for high-speed transfers
                if tcpConn, ok := conn.(*net.TCPConn); ok {
                    tcpConn.SetNoDelay(true)                // Disable Nagle's algorithm
                    tcpConn.SetReadBuffer(ReadBufferSize)   // 4MB read buffer
                    tcpConn.SetWriteBuffer(WriteBufferSize) // 4MB write buffer
                }

                return conn, nil
            },
            MaxIdleConns:          100,
            MaxIdleConnsPerHost:   100,
            MaxConnsPerHost:       100,
            IdleConnTimeout:       90 * time.Second,
            DisableCompression:    true, // Important for accurate bandwidth measurement
            ForceAttemptHTTP2:     true,
            ReadBufferSize:        ReadBufferSize,
            WriteBufferSize:       WriteBufferSize,
            ResponseHeaderTimeout: 30 * time.Second,
        },
        Timeout: 30 * time.Second,
    }
}
```

these settings match the server configuration to ensure optimal throughput measurement on high-speed connections (1 Gbps+).

### 10.2 WebRTC configuration (pion/webrtc)

```go
func newPeerConnection(turnCreds TURNCredentials) (*webrtc.PeerConnection, error) {
    config := webrtc.Configuration{
        ICEServers: []webrtc.ICEServer{{
            URLs:       turnCreds.Servers,
            Username:   turnCreds.Username,
            Credential: turnCreds.Credential,
        }},
        ICETransportPolicy: webrtc.ICETransportPolicyRelay, // Force TURN
    }

    return webrtc.NewPeerConnection(config)
}
```

### 10.3 payload generation

Upload bodies are generated lazily from a bounded reader. The reader emits zero
bytes, records first/last body reads, and participates in the load-activity
tracker. It does not allocate or retain the requested body size. Every request
sets an exact `Content-Length`, and its receipt must reconcile the same byte
count.

### 10.4 terminal detection

```go
func isInteractive() bool {
    if os.Getenv("CI") != "" {
        return false
    }
    if os.Getenv("TERM") == "dumb" {
        return false
    }
    fi, err := os.Stdout.Stat()
    if err != nil {
        return false
    }
    return fi.Mode()&os.ModeCharDevice != 0
}
```

---

## 11. loss pattern analysis

### 11.1 pattern types

```go
type LossPatternType string
const (
    LossPatternNone   LossPatternType = "none"
    LossPatternRandom LossPatternType = "random"
    LossPatternBurst  LossPatternType = "burst"
    LossPatternTail   LossPatternType = "tail"
)

type LossPattern struct {
    Type            LossPatternType
    BurstCount      int
    MaxBurstLength  int
    AvgBurstLength  float64
    EarlyLossPct    float64
    LateLossPct     float64
}
```

### 11.2 pattern detection

```go
func analyzeLossPattern(sent int, acks map[int]bool) LossPattern {
    var losses []int
    for i := 0; i < sent; i++ {
        if !acks[i] {
            losses = append(losses, i)
        }
    }

    if len(losses) == 0 {
        return LossPattern{Type: LossPatternNone}
    }

    // Detect bursts
    var bursts []int
    currentBurst := 1
    for i := 1; i < len(losses); i++ {
        if losses[i] == losses[i-1]+1 {
            currentBurst++
        } else {
            bursts = append(bursts, currentBurst)
            currentBurst = 1
        }
    }
    bursts = append(bursts, currentBurst)

    maxBurst := slices.Max(bursts)
    avgBurst := float64(sum(bursts)) / float64(len(bursts))

    // Early vs late loss
    midpoint := sent / 2
    earlyLosses := 0
    for _, seq := range losses {
        if seq < midpoint {
            earlyLosses++
        }
    }
    earlyPct := float64(earlyLosses) / float64(len(losses)) * 100
    latePct := 100 - earlyPct

    // Classify
    var patternType LossPatternType
    if maxBurst >= 10 || avgBurst > 3 {
        patternType = LossPatternBurst
    } else if latePct > 70 {
        patternType = LossPatternTail
    } else {
        patternType = LossPatternRandom
    }

    return LossPattern{
        Type:           patternType,
        BurstCount:     len(bursts),
        MaxBurstLength: maxBurst,
        AvgBurstLength: avgBurst,
        EarlyLossPct:   earlyPct,
        LateLossPct:    latePct,
    }
}
```

### 11.3 pattern display

```
Packet Loss Pattern: Burst
  Burst sequences: 3
  Max burst: 12 consecutive packets
  Avg burst: 5.3 packets
  Distribution: 23% early, 77% late
```

---

## 12. test confidence

The Go and browser clients calculate the same five-gate 0–100 score:

| gate | normal-mode requirement | deduction |
|---|---|---:|
| sample adequacy | 3 windows and 3 accepted loaded probes per enabled direction; at least 10 unloaded probes | 20 |
| variability | throughput CV below 30%; unloaded latency CV below 50% | 25 |
| loaded overlap | required probes prove uninterrupted load | 25 |
| packet test | directional server report completed and reverse-ACK loss is measurable | 20 |
| timing accuracy | no imprecise timing fallback | 10 |

Scores `80–100` are high, `50–79` medium, and lower scores low. JSON and verbose
output expose the count, coefficient-of-variation, overlap, timing, and packet
subrecords rather than a single unexplained badge. Quick mode deliberately has
fewer windows and therefore does not claim normal-mode sample adequacy.

## 13. quick mode

### 13.1 quick mode behavior

with `--quick` flag:

- use five unloaded latency probes;
- run one 1-second fixed window per enabled direction;
- target three overlap-proven loaded probes in that window;
- retain the same exact transfer and packet protocols;
- report reduced confidence rather than pretending normal sample adequacy.

```go
type QuickModeConfig struct {
    BaselineRuns       int           // 3 per 100kB/1MB baseline
    Windows            int           // 1 per enabled direction
    WindowDuration     time.Duration // 1 second
    LoadedProbeCount   int           // 3
    LatencyProbes      int           // 5
    PacketCount        int           // unchanged: 1000
}
```

### 13.2 quick mode output

```
netspeed v1.0.0 (quick mode)
Server: speed.example.com (PDX)
────────────────────────────────────────────────
  Download:   ~850 Mbps
  Upload:     ~620 Mbps
  Latency:      6 ms
  Packet Loss:  0.0% (100/100)
────────────────────────────────────────────────
Note: Quick mode uses fewer samples. Run without
--quick for more accurate results.
```

---

## 14. TURN protocol specification
## 14. packet-loss transport specification

The packet test is a WebRTC data-channel transaction carried over a TURN relay.
Clients do not implement a second measurement protocol at the raw TURN layer.
The daemon owns the answering peer and authoritative forward-path counters.

### 14.1 common client sequence

Both command-line clients:

1. fetch short-lived ICE/TURN configuration from `GET /api/turn/credentials`;
2. require at least one `turn:` or `turns:` URL;
3. create a relay-only peer connection;
4. create an unordered, unreliable data channel named `packet-loss` with zero
   retransmissions;
5. exchange a complete SDP offer/answer through
   `POST /api/packet-test/offer` using `testProfile: loss-exact-v1`;
6. send 1,000 exact 1,200-byte `NSPL` version-1 probes at 10 ms spacing;
7. wait three seconds for late acknowledgements;
8. submit transaction counts and RTT statistics to
   `POST /api/packet-test/report`;
9. validate the daemon's authoritative forward-receive,
   acknowledgement-send, duplicate, invalid, and send-failure counters;
10. report transaction, forward, and reverse-acknowledgement loss separately.

Every frame is validated against the binary layout and deterministic padding in
[`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md). Missing credentials,
TURN, WebRTC support, signaling, or authoritative counters makes packet loss
unavailable; it never becomes a measured zero.

### 14.2 Go implementation

The Go client uses Pion WebRTC. It supplies the daemon-issued ICE server URLs,
username, and credential through `webrtc.Configuration`, forces
`ICETransportPolicyRelay`, waits for ICE gathering before signaling, and uses
Pion's unordered data channel with `MaxRetransmits: 0`.

Its implementation is in `cmd/netspeed/client/webrtc.go` and the shared binary
frame implementation is in `internal/protocol`.

### 14.3 native C implementation

The C client uses libdatachannel's C API. It embeds the URL-escaped username and
credential in each TURN URL, forces `RTC_TRANSPORT_POLICY_RELAY`, disables
libdatachannel auto-negotiation so the HTTP signaling exchange owns the offer,
and creates the same unordered unreliable channel.

Its implementation is in `netspeed.c/src/packet_loss.c`. The build modes are:

- `WEBRTC=yes`: libdatachannel is required; this is mandatory for an official C
  release binary;
- `WEBRTC=auto`: use libdatachannel when discoverable and otherwise retain
  protocol-v2 throughput/latency with packet loss explicitly unavailable;
- `WEBRTC=no`: intentional HTTP-only platform bring-up and testing mode.

The C and Go clients use the same report validation constraints and JSON field
names. The complete parity and release gate is documented in
[`C_CLIENT_PARITY.md`](C_CLIENT_PARITY.md).

### 14.4 failure handling

The packet test returns an unavailable result, with a reason, when any of the
following occurs:

- the server advertises an older packet-frame version;
- no TURN relay URL is present;
- the client build lacks its WebRTC dependency;
- ICE gathering, signaling, connection, or data-channel opening fails;
- no exact-size probes can be sent;
- the report request fails;
- the daemon's directional counters are impossible or inconsistent.

Packet unavailability lowers test confidence and prevents a complete packet
quality claim, but it does not invalidate otherwise verified throughput and
latency measurements.
