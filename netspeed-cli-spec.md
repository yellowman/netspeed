# netspeed CLI - command line speed test client spec

this document specifies the **command-line client behavior** for a Go-based speed test CLI
that talks to a backend exposing these endpoints:

- `GET /meta`
- `GET /__down`
- `POST /__up`
- `GET /locations`
- `GET /api/turn/credentials`
- `POST /api/packet-test/offer`
- (optional) `POST /api/packet-test/report`

this is a **CLI-only** spec: it defines what requests the client makes,
what responses it expects, how tests are orchestrated, and how results are
displayed in the terminal. backend implementation details (TURN server config, HMAC secrets,
etc.) are intentionally out of scope.

---

## 1. command line interface

### 1.1 basic usage

```
netspeed-cli [flags] [server-url]
```

**positional arguments:**

- `server-url` - base URL of the speed test server (default: auto-detect or use config)

**examples:**

```bash
# Run full test against default server
netspeed-cli

# Run test against specific server
netspeed-cli https://speed.example.com

# Quick test (download only, fewer samples)
netspeed-cli --quick

# JSON output for scripting
netspeed-cli --json

# Verbose output with timing details
netspeed-cli -v
```

### 1.2 command line flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--server` | `-s` | auto | Server URL |
| `--quick` | `-q` | false | Quick test mode (fewer samples) |
| `--download-only` | `-d` | false | Skip upload tests |
| `--upload-only` | `-u` | false | Skip download tests |
| `--no-packet-loss` | | false | Skip packet loss test |
| `--json` | `-j` | false | Output results as JSON |
| `--csv` | | false | Output results as CSV |
| `--verbose` | `-v` | false | Show detailed progress |
| `--quiet` | | false | Minimal output (final results only) |
| `--timeout` | `-t` | 60s | Total test timeout |
| `--no-color` | | false | Disable colored output |
| `--config` | `-c` | ~/.netspeed.yaml | Config file path |
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
  "timezone": "America/Los_Angeles"
}
```

**CLI usage**

- display server and client info in header:
  ```
  Server: speed.example.com (PDX)
  Client: 203.0.113.42 (Example ISP, Inc. AS13254)
  ```

---

#### 2.1.2 `GET /__down` - download / latency

**request**

- method: `GET`
- path: `/__down`
- query parameters:
  - `bytes` (string int, optional) - number of bytes to download. `0` or omitted = latency-only probe.
  - `profile` (string, optional) - name of download profile: `100k`, `1M`, `10M`, `25M`, `100M`.
  - `run` (string int, optional) - run index within a profile.
  - `phase` (string, optional) - `unloaded`, `download`, `upload` (for latency probes).

**response**

- status: `200`
- headers:
  - `Content-Type: application/octet-stream`
  - `Content-Length: <bytes>`
- body: exactly `bytes` bytes of opaque data.

**CLI behavior**

- measure round-trip time using monotonic clock:

  ```go
  start := time.Now()
  resp, err := client.Get(url)
  // ... read body fully
  duration := time.Since(start)
  mbps := float64(received*8) / duration.Seconds() / 1e6
  ```

- for `bytes > 0`, compute throughput.
- for `bytes == 0`, use `duration` as a latency sample.

---

#### 2.1.3 `POST /__up` - upload

**request**

- method: `POST`
- path: `/__up`
- query parameters:
  - `profile` (string, optional: `100k`, `1M`, `10M`, `25M`, `50M`)
  - `run` (string int, optional)
  - `phase` (string, optional: `upload` for latency-under-load probes)
- headers:
  - `Content-Type: application/octet-stream`
- body: binary payload of the desired size.

**response**

- status: `200` (or `204`)
- body: ignored.

**CLI behavior**

- create reusable `[]byte` payload per upload profile size.
- measure duration and compute mbps from payload size.

---

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
  "testProfile": "loss-basic"
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

CLI runs tests in this sequence:

1. Fetch metadata (`/meta`)
2. Unloaded latency probes
3. Download speed tests
4. Upload speed tests
5. Loaded latency probes (during download/upload)
6. Packet loss test (WebRTC)

### 3.1 download speed tests

**profiles & sizes:**

| Profile | Size (bytes) | Runs |
|---------|--------------|------|
| 100kB | 100,000 | 10 |
| 1MB | 1,000,000 | 8 |
| 10MB | 10,000,000 | 6 |
| 25MB | 25,000,000 | 4 |
| 100MB | 100,000,000 | 3 |
| ... | ... | ... |

**per profile:**

```go
url := fmt.Sprintf("%s/__down?bytes=%d&profile=%s&run=%d", baseURL, sizeBytes, profile, runIndex)
start := time.Now()
resp, _ := client.Get(url)
received, _ := io.Copy(io.Discard, resp.Body)
duration := time.Since(start)
mbps := float64(received*8) / duration.Seconds() / 1e6
```

---

### 3.2 upload speed tests

**profiles & sizes:**

| Profile | Size (bytes) | Runs |
|---------|--------------|------|
| 100kB | 100,000 | 8 |
| 1MB | 1,000,000 | 6 |
| 10MB | 10,000,000 | 4 |
| 25MB | 25,000,000 | 4 |
| 50MB | 50,000,000 | 3 |
| ... | ... | ... |

**per profile:**

```go
url := fmt.Sprintf("%s/__up?profile=%s&run=%d", baseURL, profile, runIndex)
start := time.Now()
resp, _ := client.Post(url, "application/octet-stream", bytes.NewReader(payload))
_ = resp.Body.Close()
duration := time.Since(start)
mbps := float64(len(payload)*8) / duration.Seconds() / 1e6
```

---

### 3.3 latency tests

**phases:**

1. **unloaded latency** - 20 probes with adaptive batching
2. **latency during download** - 5 probes during large download
3. **latency during upload** - 5 probes during large upload

**adaptive batching:**

```go
// Phase 1: run first 3 probes sequentially
for i := 0; i < 3; i++ {
    sample := measureLatency(baseURL, "unloaded", i)
    samples = append(samples, sample)
}

// Phase 2: decide strategy based on median RTT
medianRTT := calculateMedian(samples)

var useParallel bool
if medianRTT < 50*time.Millisecond {
    useParallel = true
} else if medianRTT >= 100*time.Millisecond {
    // High latency: check bandwidth
    bandwidth := quickBandwidthEstimate(baseURL)
    useParallel = bandwidth >= 2.0 // Mbps
} else {
    useParallel = true // 50-100ms
}

// Phase 3: run remaining 17 probes
if useParallel {
    // Batch 5 probes at a time using goroutines
} else {
    // Sequential probes
}
```

---

### 3.4 packet loss test (WebRTC)

**steps:**

1. Fetch TURN credentials from `/api/turn/credentials`
2. Create `pion/webrtc` peer connection with ICE servers
3. Create data channel labeled `"packet-loss"`
4. Exchange SDP via `/api/packet-test/offer`
5. Send 1000 packets at 10ms intervals
6. Wait 3 seconds for late acks
7. Compute loss statistics

```go
const (
    numPackets  = 1000
    intervalMs  = 10
    extraWaitMs = 3000
)

// Send packets
ticker := time.NewTicker(10 * time.Millisecond)
for seq := 0; seq < numPackets; seq++ {
    msg := PacketMessage{Seq: seq, SentAt: time.Now().UnixMilli()}
    dc.SendText(json.Marshal(msg))
    <-ticker.C
}

// Wait for late acks
time.Sleep(3 * time.Second)

// Calculate results
lossPercent := float64(numPackets-len(acks)) / float64(numPackets) * 100
```

---

## 4. CLI data model

```go
type LatencyPhase string
const (
    PhaseUnloaded LatencyPhase = "unloaded"
    PhaseDownload LatencyPhase = "download"
    PhaseUpload   LatencyPhase = "upload"
)

type LatencySample struct {
    Timestamp time.Time
    RTT       time.Duration
    Phase     LatencyPhase
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
netspeed-cli v1.0.0
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
      "phase": "unloaded"
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
    Phase   string // "meta", "download", "upload", "latency", "packet-loss"
    Message string
    Err     error
}

func (e *TestError) Error() string {
    return fmt.Sprintf("%s: %s", e.Phase, e.Message)
}
```

**error output:**

```
Error: Failed to connect to server
  Server: speed.example.com
  Reason: connection refused

Retry with: netspeed-cli -s https://speed.example.com
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

## 7. adaptive profile selection

### 7.1 time budget constants

```go
const (
    MaxTestDuration      = 4 * time.Second  // max time for single profile selection
    TotalDownloadBudget  = 8 * time.Second  // total download phase budget
    TotalUploadBudget    = 8 * time.Second  // total upload phase budget
)
```

### 7.2 profile selection

```go
func estimateTransferTime(bytes int64, speedMbps float64) time.Duration {
    if speedMbps <= 0 {
        return time.Hour // effectively infinite
    }
    seconds := float64(bytes*8) / (speedMbps * 1e6)
    return time.Duration(seconds * float64(time.Second))
}

func selectProfiles(estimatedSpeed float64, allProfiles map[string]Profile) map[string]Profile {
    selected := map[string]Profile{
        "100kB": allProfiles["100kB"],
        "1MB":   allProfiles["1MB"],
    }

    for name, profile := range allProfiles {
        if name == "100kB" || name == "1MB" {
            continue
        }
        if estimateTransferTime(profile.Bytes, estimatedSpeed) <= MaxTestDuration {
            selected[name] = profile
        }
    }

    return selected
}
```

### 7.3 batch skipping

```go
// During test execution
for _, profile := range selectedProfiles {
    elapsed := time.Since(phaseStart)
    remaining := TotalDownloadBudget - elapsed
    estimatedBatch := estimateTransferTime(profile.Bytes, currentSpeed) * time.Duration(profile.Runs)

    if estimatedBatch > remaining {
        if verbose {
            fmt.Printf("  Skipping %s (insufficient time)\n", profile.Name)
        }
        continue
    }

    // Run profile tests
}
```

---

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

## 9. configuration file

### 9.1 config file format

`~/.netspeed.yaml`:

```yaml
# Default server
server: https://speed.example.com

# Output preferences
color: true
verbose: false

# Test settings
timeout: 60s
skip_packet_loss: false

# Quick mode settings
quick:
  download_runs: 3
  upload_runs: 3
  latency_probes: 5
```

### 9.2 config loading

```go
type Config struct {
    Server         string        `yaml:"server"`
    Color          bool          `yaml:"color"`
    Verbose        bool          `yaml:"verbose"`
    Timeout        time.Duration `yaml:"timeout"`
    SkipPacketLoss bool          `yaml:"skip_packet_loss"`
    Quick          QuickConfig   `yaml:"quick"`
}

type QuickConfig struct {
    DownloadRuns   int `yaml:"download_runs"`
    UploadRuns     int `yaml:"upload_runs"`
    LatencyProbes  int `yaml:"latency_probes"`
}

func LoadConfig(path string) (*Config, error) {
    // Default config
    cfg := &Config{
        Color:   true,
        Timeout: 60 * time.Second,
        Quick: QuickConfig{
            DownloadRuns:  3,
            UploadRuns:    3,
            LatencyProbes: 5,
        },
    }

    // Override from file if exists
    if data, err := os.ReadFile(path); err == nil {
        yaml.Unmarshal(data, cfg)
    }

    return cfg, nil
}
```

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

```go
var payloadCache = make(map[int64][]byte)
var payloadMu sync.Mutex

func getPayload(size int64) []byte {
    payloadMu.Lock()
    defer payloadMu.Unlock()

    if payload, ok := payloadCache[size]; ok {
        return payload
    }

    payload := make([]byte, size)
    // Fill with random data to prevent compression
    rand.Read(payload)
    payloadCache[size] = payload

    return payload
}
```

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

### 12.1 confidence assessment

```go
type ConfidenceLevel string
const (
    ConfidenceHigh   ConfidenceLevel = "high"
    ConfidenceMedium ConfidenceLevel = "medium"
    ConfidenceLow    ConfidenceLevel = "low"
)

type TestConfidence struct {
    Level       ConfidenceLevel
    Score       int // 0-100
    Warnings    []string
    SampleCount struct {
        Download int
        Upload   int
        Latency  int
    }
}

func assessConfidence(samples []ThroughputSample, latency []LatencySample, packetLoss *PacketLossResult) TestConfidence {
    var warnings []string
    score := 100

    dlCount := countByDirection(samples, DirectionDownload)
    ulCount := countByDirection(samples, DirectionUpload)
    latCount := len(latency)

    if dlCount < 20 {
        score -= 20
        warnings = append(warnings, "Insufficient download samples")
    }
    if ulCount < 15 {
        score -= 15
        warnings = append(warnings, "Insufficient upload samples")
    }
    if latCount < 10 {
        score -= 15
        warnings = append(warnings, "Insufficient latency samples")
    }
    if packetLoss == nil || packetLoss.Unavailable {
        score -= 15
        warnings = append(warnings, "Packet loss test incomplete")
    }

    // Check coefficient of variation
    dlCV := coefficientOfVariation(filterByDirection(samples, DirectionDownload))
    if dlCV > 30 {
        score -= 20
        warnings = append(warnings, "High download variability")
    }

    var level ConfidenceLevel
    if score >= 80 {
        level = ConfidenceHigh
    } else if score >= 50 {
        level = ConfidenceMedium
    } else {
        level = ConfidenceLow
    }

    return TestConfidence{
        Level:    level,
        Score:    max(0, score),
        Warnings: warnings,
        SampleCount: struct{ Download, Upload, Latency int }{
            Download: dlCount,
            Upload:   ulCount,
            Latency:  latCount,
        },
    }
}
```

### 12.2 confidence display

```
Test Confidence: High (92/100)
  Samples: Download 31, Upload 25, Latency 18
```

or with warnings:

```
Test Confidence: Medium (65/100)
  Samples: Download 12, Upload 8, Latency 5
  Warnings:
    - Insufficient download samples
    - High download variability
```

---

## 13. quick mode

### 13.1 quick mode behavior

with `--quick` flag:

- reduce sample counts to minimum viable
- skip larger profiles
- shorter timeouts

```go
type QuickModeConfig struct {
    DownloadProfiles []string      // ["100kB", "1MB"]
    DownloadRuns     int           // 3
    UploadProfiles   []string      // ["100kB", "1MB"]
    UploadRuns       int           // 3
    LatencyProbes    int           // 5
    PacketCount      int           // 100
    SkipLoadedLatency bool         // true
}
```

### 13.2 quick mode output

```
netspeed-cli v1.0.0 (quick mode)
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
