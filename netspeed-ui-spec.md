# netspeed UI - speedtest frontend spec (http + TURN)

this document specifies the **browser-side behavior and ui** for a speedtest app
that talks to a backend exposing these endpoints:

- `GET /meta`
- `GET /__down`
- `POST /__up`
- `GET /locations`
- `GET /api/turn/credentials`
- `POST /api/packet-test/offer`
- `POST /api/packet-test/report`

this is a **frontend-only** spec: it defines what requests the browser makes,
what responses it expects, how tests are orchestrated, and how results are
displayed. backend implementation details (turn server config, hmac secrets,
etc.) are intentionally out of scope.

---


> **Phase 2 authority:** the implemented browser requires measurement protocol
> version 2. [`MEASUREMENT_PROTOCOL_V2.md`](MEASUREMENT_PROTOCOL_V2.md) is the
> canonical wire and methodology contract. It supersedes pre-v2 giant-profile,
> post-transfer loaded-latency, short JSON packet, and large retained-buffer
> examples in earlier revisions of this design document.

## 1. backend api surface (from the frontend's point of view)

### 1.1 http measurement endpoints

#### 1.1.1 `GET /meta` - client / server metadata

**request**

- method: `GET`
- path: `/meta`
- query params: none (must tolerate extras)
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
  "measurementProtocolVersion": 2,
  "uploadReceiptVersion": 1,
  "packetLossFrameVersion": 1
}
```

**frontend usage**

- populate **server location** card:
  - “Server location: `<city>`”
  - “Your network: `<asOrganization> (AS<asn>)`”
  - “Your IP address: `<clientIp>`”
- use `latitude` / `longitude` and `colo` to place markers and draw lines on the map.

---

#### 1.1.2 `GET /__down` — download / latency

The browser requests an exact byte count plus unique `measId` and diagnostic
`profile`, `run`, and `during` labels. It accepts only status `200`, binary
content type, a matching supplied `Content-Length`, an exact body byte count,
and a positive duration.

Streaming response bodies are consumed incrementally. Where response streaming
is unavailable, the browser limits a materialized fallback to 100 MB. Throughput
request timing uses Resource Timing when available; aggregate fixed-window speed
uses verified bytes over the window wall-clock interval. A zero-byte request is
a latency probe.

#### 1.1.3 `POST /__up` — verified upload

The browser sends an exact-length binary body with unique `measId` and diagnostic
`profile` and `run` labels. A successful response is receipt version 1:

```json
{
  "ok": true,
  "acceptedBytes": 1000000,
  "serverDurationNs": 123456789
}
```

The sample is retained only when status and JSON type are correct and
`acceptedBytes` equals the intended body length. The daemon duration is
canonical. A streaming-capable browser emits 64 KiB request chunks. Other
browsers reuse a payload no larger than 8 MiB and use XHR upload lifecycle events
to identify actual outbound load; receipt wait is not upload load.

#### 1.1.4 `GET /locations` — test locations

**request**

- method: `GET`
- path: `/locations`
- query params: none
- body: none

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
  },
  {
    "iata": "SEA",
    "lat": 47.4502,
    "lon": -122.3088,
    "cca2": "US",
    "region": "North America",
    "city": "Seattle"
  }
]
```

**frontend usage**

- match `meta.colo` to `Location.iata` to find the active server location.
- feed into the map to display the server marker.
- optionally use additional locations to show alternative test sites.

---

### 1.2 TURN & WebRTC endpoints (frontend contract only)

#### 1.2.1 `GET /api/turn/credentials`

**purpose:** obtain temporary TURN credentials and ICE server URLs for WebRTC.

**request**

- method: `GET`
- path: `/api/turn/credentials`
- credentials: include cookies/session:

  ```ts
  fetch('/api/turn/credentials', { credentials: 'include' });
  ```

- query parameters:
  - `ttl` (optional int seconds) — hint for desired credential lifetime.

**response (200)**

```json
{
  "username": "1701532800:abcd1234",
  "credential": "opaque-password-string",
  "ttlSec": 600,
  "servers": [
    "stun:turn1.example.com:3478",
    "turn:turn1.example.com:3478?transport=udp",
    "turn:turn1.example.com:3478?transport=tcp",
    "turns:turns1.example.com:5349?transport=tcp"
  ],
  "realm": "speed.example.com"
}
```

- `username`, `credential`, `realm` are opaque to the frontend.
- `servers` is used as `iceServers[].urls`.

**frontend usage**

```ts
const res = await fetch('/api/turn/credentials', { credentials: 'include' });
const turn = await res.json();

const pc = new RTCPeerConnection({
  iceServers: [{
    urls: turn.servers,
    username: turn.username,
    credential: turn.credential
  }],
  iceTransportPolicy: 'relay' // prefer TURN
});
```

the frontend must tolerate additional fields in the response.

---

#### 1.2.2 `POST /api/packet-test/offer`

**purpose:** send the browser’s WebRTC offer, receive an answer and test id.

**request**

- method: `POST`
- path: `/api/packet-test/offer`
- headers: `Content-Type: application/json`
- body:

```json
{
  "sdp": "<browser-offer-sdp>",
  "type": "offer",
  "testProfile": "loss-exact-v1"
}
```

**response (200)**

```json
{
  "sdp": "<server-answer-sdp>",
  "type": "answer",
  "testId": "c65b0b1d-6f7f-4a9a-9f2b-7c9d3c5f0c3a"
}
```

**frontend usage**

```ts
const offer = await pc.createOffer();
await pc.setLocalDescription(offer);

const res = await fetch('/api/packet-test/offer', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  credentials: 'include',
  body: JSON.stringify({
    sdp: offer.sdp,
    type: offer.type,
    testProfile: 'loss-exact-v1'
  })
});

const { sdp, type, testId } = await res.json();
await pc.setRemoteDescription(new RTCSessionDescription({ sdp, type }));

// store testId alongside results (optional)
```

the frontend assumes the server will create a corresponding peer connection and
respond with a valid WebRTC answer.

---

#### 1.2.3 `POST /api/packet-test/report`

This report is required for a complete protocol-v2 packet test because it returns
the daemon's authoritative forward-path counters.

**request body**

```json
{
  "testId": "c65b0b1d-6f7f-4a9a-9f2b-7c9d3c5f0c3a",
  "sent": 1000,
  "received": 995,
  "lossPercent": 0.5,
  "rttMinMs": 15,
  "rttMedianMs": 20,
  "rttP90Ms": 30,
  "jitterMs": 10
}
```

**accepted response**

```json
{
  "ok": true,
  "protocolVersion": 2,
  "frameSizeBytes": 1200,
  "forwardReceived": 998,
  "acknowledgementsSent": 998,
  "duplicateFrames": 0,
  "invalidFrames": 0,
  "ackSendFailures": 0
}
```

The browser reconciles these counts with its unique acknowledgements before it
publishes transaction, forward, and reverse-acknowledgement loss. A missing or
invalid report makes packet loss unavailable rather than assuming zero.

## 2. measurement pipeline (frontend behavior)

The browser executes:

1. capability negotiation and metadata;
2. 20 unloaded latency probes with adaptive batching;
3. three verified 100 kB and three verified 1 MB download baselines;
4. three 1.5-second sustained download windows, with loaded probes in the middle
   window;
5. matching upload baselines and sustained windows;
6. the exact-frame WebRTC packet test;
7. shared summaries, grades, diagnostics, and confidence.

### 2.1 bounded throughput windows

The median 1 MB baseline selects a request chunk intended to last about 250 ms
per flow. The chunk is 100,000 bytes through 256 MiB and is always capped by
`maxTransferBytes` and browser fallback limits. Flow count rises from 1 to 2, 4,
or 6 as the estimate increases. Each worker repeatedly completes exact verified
requests until the window owner stops it.

A window sample is completed verified bytes divided by elapsed wall-clock time.
Baseline requests tune the plan but are excluded from headline throughput when
window samples exist.

### 2.2 continuous loaded-latency proof

The window owns an aggregate active-transfer tracker. A probe is retained only
when at least one transfer is active before and after the probe and no zero-load
gap occurs between those observations. A rejected probe is retried.

Download activity spans response-body reads. Streaming upload activity spans
request-stream production. XHR fallback activity spans browser upload start/end
events. Buffered fetch fallback is marked imprecise for confidence purposes.
Normal mode targets five probes and requires at least three accepted probes for
each enabled direction. The window timer stops new requests and further probe
retries, then drains requests already in flight. When the timer expires, a
loaded-latency result is retained only if its accepted-probe quorum has already
been reached; otherwise the direction fails instead of stretching the nominal
window indefinitely.

### 2.3 exact packet test

The browser uses an unordered data channel with `maxRetransmits: 0` and sends
1,000 exact 1,200-byte binary `NSPL` frames at 10 ms intervals. Local `send()`
failures do not consume sequence numbers. The daemon validates frame version,
length, header, type, and deterministic padding and acknowledges each unique
valid probe once.

The browser waits for late acknowledgements, then obtains the daemon report and
publishes round-trip transaction loss, forward probe loss, and reverse
acknowledgement loss separately.

### 2.4 shared statistics

The frontend mirrors `internal/measurement`: finite positive values, warmup
removal, R-7 percentiles, conservative 1.5-IQR filtering, p90-minus-median
jitter, and population coefficient of variation.

## 3. frontend data model

```ts
type LatencySample = {
  ts: number;
  rttMs: number;
  phase: 'unloaded' | 'download' | 'upload';
  probeStartedAt?: number;
  probeFinishedAt?: number;
  loadOverlapped?: boolean;
  loadTrackingAccurate?: boolean;
  timingSource?: string;
};

type ThroughputSample = {
  ts: number;
  direction: 'download' | 'upload';
  sizeBytes: number;
  durationMs: number;
  mbps: number;
  profile: string;
  runIndex: number;
  sampleKind?: 'baseline' | 'window';
  concurrency?: number;
  chunkBytes?: number;
  requestCount?: number;
  timingSource?: string;
};

type PacketLossResult = {
  sent: number;
  received: number;
  lossPercent: number | null; // transaction-loss compatibility alias
  transactionLossPercent: number | null;
  forwardSent: number;
  forwardReceived: number;
  forwardLossPercent: number | null;
  acknowledgementsSent: number;
  acknowledgementsReceived: number;
  reverseAcknowledgementLossPercent: number | null;
  frameSizeBytes: number;
  duplicateFrames: number;
  invalidFrames: number;
  ackSendFailures: number;
  rttStatsMs: { min: number; median: number; p90: number };
  jitterMs: number;
  testId?: string;
  unavailable?: boolean;
  reason?: string;
};

type Summary = {
  downloadMbps: number;
  uploadMbps: number;
  latencyUnloadedMs: number;
  latencyDownloadMs: number;
  latencyUploadMs: number;
  jitterMs: number;
  packetLossPercent: number | null;
};

type Meta = {
  hostname: string;
  clientIp: string;
  httpProtocol: string;
  asn: number;
  asOrganization: string;
  colo: string;
  country: string;
  city: string;
  region: string;
  postalCode: string;
  latitude: number;
  longitude: number;
  timezone?: string;
  maxTransferBytes: number;
  measurementProtocolVersion: number;
  uploadReceiptVersion: number;
  packetLossFrameVersion: number;
};
```

The existing location, map, diagnostic, grading, and presentation types remain
unchanged.

## 4. summary metrics & grading

### 4.1 summary calculations

- Download and upload are the R-7 p90 of fixed-window values after conservative
  IQR filtering. Baselines are excluded whenever window samples exist.
- Unloaded latency drops two warmup samples, filters, and uses the median.
- Loaded latency uses only `loadOverlapped === true` probes and reports the
  median of each direction.
- Jitter is unloaded p90 minus unloaded median.
- `packetLossPercent` is transaction loss when the exact-frame test completed;
  otherwise it is `null`.

All percentiles use R-7 interpolation. Unknown packet loss is never represented
as zero, and grades requiring packet loss are `Incomplete`.

### 4.2 network quality grading (example)

```ts
function gradeForStreaming(s: Summary): NetworkQualityGrade {
  const { downloadMbps, latencyUnloadedMs, jitterMs, packetLossPercent } = s;
  if (packetLossPercent === null) return 'Incomplete';

  if (
    downloadMbps >= 50 &&
    latencyUnloadedMs <= 25 &&
    jitterMs <= 5 &&
    packetLossPercent <= 0.5
  ) return 'Great';

  if (
    downloadMbps >= 20 &&
    latencyUnloadedMs <= 50 &&
    jitterMs <= 15 &&
    packetLossPercent <= 1.5
  ) return 'Good';

  if (
    downloadMbps >= 10 &&
    latencyUnloadedMs <= 80 &&
    jitterMs <= 30 &&
    packetLossPercent <= 3
  ) return 'Okay';

  return 'Poor';
}
```

gaming / video chat grades can reuse this with stricter latency/jitter thresholds.

---

## 5. ui layout (single-page app)

the visual layout mirrors the reference speedtest page while using the data
model and metrics above.

### 5.1 top bar

- left: wordmark Speed Test.
- right: text built on netspeed linking to https://github.com/yellowman/netspeed
- optional sticky behavior.

---

### 5.2 hero metrics

three main columns on desktop, stacked on mobile.

**download column**

- large number: `Summary.downloadMbps` rounded.
- label: “Download”.
- unit label: “Mbps”.
- small info icon with tooltip.
- sparkline of download mbps over time.

**upload column**

- same layout using `Summary.uploadMbps`.

**latency column**

- main number: `Summary.latencyUnloadedMs` (e.g. `6.0 ms`).
- secondary line:
  - “Jitter: `Summary.jitterMs` ms”
  - “Packet Loss: `Summary.packetLossPercent`%”
- small timestamp: “Measured at HH:MM:SS”.

below columns:

- `Pause` button (temporarily halts remaining tests).
- `Retest` button (resets state and reruns everything).
- icons:
  - share (copy link)
  - download results (json).

---

### 5.3 network quality score

section title: “Network Quality Score” with “Learn more” link.

three columns:

- **Video Streaming**
- **Online Gaming**
- **Video Chatting**

each column shows:

- colored dot (green/yellow/red) based on grade.
- text label (`Great` / `Good` / `Okay` / `Poor`).

clicking “Learn more” opens a modal explaining threshold bands.

---

### 5.4 server location & latency

row with two main columns.

**left: server location card**

- map component (e.g. Leaflet, Mapbox).
- shows:
  - server marker at `Location.lat/lon` derived from `meta.colo`.
  - optional client marker at `meta.latitude/meta.longitude`.
  - line between client and server.

- text bullets:

  - “Connected via: IPv4/IPv6” (from `clientIp` heuristic).
  - “Server location: `<location.city>`”.
  - “Your network: `<asOrganization> (AS<asn>)`”.
  - “Your IP address: `<clientIp>`”.

**right: latency cards**

three accordion-style cards:

1. **Unloaded latency (20/20)**

   - collapsed header: title and quick summary (min/median/max).
   - expanded:
     - small chart of all `phase='unloaded'` samples.
     - explicit list of min/median/max.

2. **Latency during download (5)**

   - header shows number of samples.
   - expanded:
     - chart for `phase='download'` samples.
     - table:

       | # | Ping |
       |---|------|
       | 1 | 6 ms |
       | 2 | 41 ms |
       | … | …    |

3. **Latency during upload (N)**

   - identical layout using `phase='upload'` samples.

---

### 5.5 packet loss measurements

single card.

- header: “Packet Loss Test (`received`/`sent`)".
- horizontal bar:
  - green length = `received / sent`.
  - gray remainder for lost packets.
- details:

  - “Packet Loss: `lossPercent`%”.
  - “Received: `received / sent` packets”.
  - “Method: TURN/WebRTC DataChannel”.

expanded view shows:

- RTT stats (min / median / p90).
- jitter estimate.
- TURN server/transport string (from WebRTC stats if available).

---

### 5.6 download measurements grid

grid of cards, 2 columns on desktop.

for each download profile:

- header: e.g. “100kB download test (10/10)” (`runsComplete/total`).
- collapsed view:
  - tiny chart of mbps per run.
- expanded view:
  - table with columns `#`, `Duration`, `Speed`.

---

### 5.7 upload measurements grid

identical layout for upload profiles:

- “100kB upload test”, “1MB upload test”, …, “50MB upload test”.

---

### 5.8 footer

simple footer with:

- links: `Home`, `About`, `Privacy Policy`, `Terms of Use`.
- right-aligned logo for your brand.

---

## 6. frontend flow (state machine)

1. **idle**
   - on initial load:
     - fetch `/meta` and `/locations` in parallel.
     - render top section with placeholder values.
   - show “Start test” button.

2. **running**
   - when “Start test” or “Retest” is clicked:
     - clear previous results.
     - disable “Retest”.
     - sequence:
       1. unloaded latency baseline.
       2. quick small download/upload warmup.
       3. full download profiles.
       4. full upload profiles.
       5. latency-under-download and latency-under-upload probes.
       6. TURN + WebRTC packet loss test.
     - update UI progressively as each segment finishes.

3. **complete**
   - compute `Summary` + `NetworkQuality`.
   - enable “Retest”.
   - allow “Download results as JSON” and “Copy link”.

4. **error**
   - if a test segment fails:
     - mark that card as "Test failed" with a tooltip.
     - still compute partial summary from available data.
   - if TURN / WebRTC fails:
     - show "Packet loss test unavailable" placeholder.
     - do not block other metrics.

---

## 7. extended location display

### 7.1 client location details

display additional location information from `/meta` response:

```ts
type ExtendedLocation = {
  country: string;      // ISO country code (e.g., "US")
  region: string;       // State/province (e.g., "Oregon")
  city: string;         // City name (e.g., "Bend")
  postalCode: string;   // ZIP/postal code
  timezone: string;     // IANA timezone (e.g., "America/Los_Angeles")
  latitude: number;
  longitude: number;
};
```

**ui layout:**

in the server location card, add expanded client details:

- "Location: `<city>`, `<region>`, `<country>`"
- "Timezone: `<timezone>`"
- "Coordinates: `<latitude>°, <longitude>°`"
- "Distance to server: `<calculated_distance>` km"

**distance calculation:**

```ts
function haversineDistance(lat1: number, lon1: number, lat2: number, lon2: number): number {
  const R = 6371; // Earth's radius in km
  const dLat = (lat2 - lat1) * Math.PI / 180;
  const dLon = (lon2 - lon1) * Math.PI / 180;
  const a = Math.sin(dLat/2) * Math.sin(dLat/2) +
            Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) *
            Math.sin(dLon/2) * Math.sin(dLon/2);
  const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1-a));
  return R * c;
}
```

---

## 8. timing breakdown

### 8.1 request timing metrics

capture detailed timing from the Resource Timing API for each request:

```ts
type TimingBreakdown = {
  dnsMs: number;        // domainLookupEnd - domainLookupStart
  tcpMs: number;        // connectEnd - connectStart
  tlsMs: number;        // connectEnd - secureConnectionStart (if HTTPS)
  ttfbMs: number;       // responseStart - requestStart (time to first byte)
  transferMs: number;   // responseEnd - responseStart (body transfer time)
  totalMs: number;      // responseEnd - fetchStart
};
```

**collection:**

```ts
function extractTiming(entry: PerformanceResourceTiming): TimingBreakdown {
  return {
    dnsMs: entry.domainLookupEnd - entry.domainLookupStart,
    tcpMs: entry.connectEnd - entry.connectStart,
    tlsMs: entry.secureConnectionStart > 0
           ? entry.connectEnd - entry.secureConnectionStart
           : 0,
    ttfbMs: entry.responseStart - entry.requestStart,
    transferMs: entry.responseEnd - entry.responseStart,
    totalMs: entry.responseEnd - entry.fetchStart
  };
}
```

**ui display:**

show timing breakdown in a dedicated section with horizontal stacked bars:

```
Request Timing Breakdown
├── DNS Lookup:    2.3 ms  ████
├── TCP Connect:   8.1 ms  ████████
├── TLS Handshake: 12.4 ms ████████████
├── TTFB:          6.2 ms  ██████
└── Transfer:      45.1 ms ██████████████████████████████████████████
```

aggregate across all requests to show:
- average timing per phase
- min/max timing per phase
- percentage of total time spent in each phase

---

## 9. packet loss pattern analysis

### 9.1 loss pattern detection

analyze packet loss to distinguish between random loss and burst loss:

```ts
type LossPattern = {
  type: 'random' | 'burst' | 'tail' | 'none';
  burstCount: number;           // number of consecutive loss sequences
  maxBurstLength: number;       // longest consecutive packet loss
  avgBurstLength: number;       // average burst length
  lossDistribution: number[];   // histogram of loss positions (10 buckets)
  earlyLossPercent: number;     // % of losses in first half
  lateLossPercent: number;      // % of losses in second half
};
```

**detection algorithm:**

```ts
function analyzeLossPattern(sent: number, acks: Set<number>): LossPattern {
  const losses: number[] = [];
  for (let i = 0; i < sent; i++) {
    if (!acks.has(i)) losses.push(i);
  }

  if (losses.length === 0) {
    return { type: 'none', burstCount: 0, maxBurstLength: 0, avgBurstLength: 0,
             lossDistribution: new Array(10).fill(0), earlyLossPercent: 0, lateLossPercent: 0 };
  }

  // Detect bursts (consecutive losses)
  const bursts: number[] = [];
  let currentBurst = 1;
  for (let i = 1; i < losses.length; i++) {
    if (losses[i] === losses[i-1] + 1) {
      currentBurst++;
    } else {
      bursts.push(currentBurst);
      currentBurst = 1;
    }
  }
  bursts.push(currentBurst);

  const maxBurstLength = Math.max(...bursts);
  const avgBurstLength = bursts.reduce((a, b) => a + b, 0) / bursts.length;

  // Calculate distribution across test duration
  const bucketSize = sent / 10;
  const distribution = new Array(10).fill(0);
  losses.forEach(seq => {
    const bucket = Math.min(9, Math.floor(seq / bucketSize));
    distribution[bucket]++;
  });

  const midpoint = sent / 2;
  const earlyLosses = losses.filter(s => s < midpoint).length;
  const earlyLossPercent = (earlyLosses / losses.length) * 100;
  const lateLossPercent = 100 - earlyLossPercent;

  // Classify pattern
  let type: 'random' | 'burst' | 'tail' | 'none';
  if (maxBurstLength >= 10 || avgBurstLength > 3) {
    type = 'burst';
  } else if (lateLossPercent > 70) {
    type = 'tail';  // Connection degradation toward end
  } else {
    type = 'random';
  }

  return {
    type,
    burstCount: bursts.length,
    maxBurstLength,
    avgBurstLength,
    lossDistribution: distribution,
    earlyLossPercent,
    lateLossPercent
  };
}
```

**ui display:**

show packet loss analysis card with:

- loss type badge: "Random Loss" (yellow), "Burst Loss" (red), "Tail Loss" (orange), "No Loss" (green)
- loss timeline visualization (horizontal bar divided into 10 segments, colored by loss density)
- burst statistics:
  - "Burst count: N sequences"
  - "Max burst: N consecutive packets"
  - "Avg burst: N.N packets"
- distribution chart showing loss density across test duration

---

## 10. data channel statistics

### 10.1 webrtc data channel metrics

collect statistics from the WebRTC data channel during packet loss test:

```ts
type DataChannelStats = {
  // Connection info
  connectionType: 'host' | 'srflx' | 'prflx' | 'relay';
  localCandidateType: string;
  remoteCandidateType: string;
  protocol: 'udp' | 'tcp';

  // Throughput
  bytesSent: number;
  bytesReceived: number;
  messagesSent: number;
  messagesReceived: number;

  // Timing
  connectionSetupMs: number;     // time from offer to data channel open
  iceGatheringMs: number;        // time for ICE gathering
  dtlsHandshakeMs: number;       // DTLS handshake duration

  // Quality
  availableOutgoingBitrate?: number;  // estimated available bandwidth
  currentRoundTripTime?: number;      // current RTT from ICE
};
```

**collection from RTCPeerConnection:**

```ts
async function collectDataChannelStats(pc: RTCPeerConnection): Promise<DataChannelStats> {
  const stats = await pc.getStats();

  let connectionType = 'unknown';
  let localCandidateType = '';
  let remoteCandidateType = '';
  let protocol = 'udp';
  let bytesSent = 0;
  let bytesReceived = 0;
  let messagesSent = 0;
  let messagesReceived = 0;
  let availableOutgoingBitrate;
  let currentRoundTripTime;

  stats.forEach(report => {
    if (report.type === 'candidate-pair' && report.nominated) {
      currentRoundTripTime = report.currentRoundTripTime * 1000; // to ms
      availableOutgoingBitrate = report.availableOutgoingBitrate;
    }

    if (report.type === 'local-candidate' && report.isRemote === false) {
      localCandidateType = report.candidateType;
      protocol = report.protocol;
    }

    if (report.type === 'remote-candidate') {
      remoteCandidateType = report.candidateType;
    }

    if (report.type === 'data-channel') {
      bytesSent = report.bytesSent;
      bytesReceived = report.bytesReceived;
      messagesSent = report.messagesSent;
      messagesReceived = report.messagesReceived;
    }
  });

  // Determine connection type based on candidate types
  if (localCandidateType === 'relay' || remoteCandidateType === 'relay') {
    connectionType = 'relay';  // TURN server used
  } else if (localCandidateType === 'srflx' || remoteCandidateType === 'srflx') {
    connectionType = 'srflx';  // STUN server used (NAT traversal)
  } else if (localCandidateType === 'prflx' || remoteCandidateType === 'prflx') {
    connectionType = 'prflx';  // Peer reflexive
  } else {
    connectionType = 'host';   // Direct connection
  }

  return {
    connectionType,
    localCandidateType,
    remoteCandidateType,
    protocol,
    bytesSent,
    bytesReceived,
    messagesSent,
    messagesReceived,
    connectionSetupMs: 0,  // calculated separately
    iceGatheringMs: 0,     // calculated separately
    dtlsHandshakeMs: 0,    // calculated separately
    availableOutgoingBitrate,
    currentRoundTripTime
  };
}
```

**ui display:**

show data channel stats in packet loss section:

- connection path indicator: "Direct" / "STUN (NAT)" / "TURN Relay"
- protocol badge: "UDP" / "TCP"
- data transferred: "Sent: X.X KB, Received: X.X KB"
- connection timing breakdown:
  - "ICE Gathering: X ms"
  - "DTLS Handshake: X ms"
  - "Total Setup: X ms"

---

## 11. bandwidth estimation

### 11.1 available bandwidth metrics

estimate available bandwidth from throughput samples and WebRTC stats:

```ts
type BandwidthEstimate = {
  // Raw estimates
  downloadPeakMbps: number;      // maximum observed download
  downloadSustainedMbps: number; // 75th percentile download
  uploadPeakMbps: number;        // maximum observed upload
  uploadSustainedMbps: number;   // 75th percentile upload

  // WebRTC-based estimate (if available)
  webrtcEstimateMbps?: number;

  // Stability metrics
  downloadVariability: number;   // coefficient of variation (std/mean)
  uploadVariability: number;

  // Trend analysis
  downloadTrend: 'stable' | 'improving' | 'degrading';
  uploadTrend: 'stable' | 'improving' | 'degrading';
};
```

**calculation:**

```ts
function estimateBandwidth(samples: ThroughputSample[]): BandwidthEstimate {
  const dlSamples = samples.filter(s => s.direction === 'download').map(s => s.mbps);
  const ulSamples = samples.filter(s => s.direction === 'upload').map(s => s.mbps);

  function stats(arr: number[]) {
    if (arr.length === 0) return { peak: 0, sustained: 0, variability: 0, trend: 'stable' as const };

    const sorted = [...arr].sort((a, b) => a - b);
    const peak = Math.max(...arr);
    const sustained = sorted[Math.floor(sorted.length * 0.75)] || peak;

    const mean = arr.reduce((a, b) => a + b, 0) / arr.length;
    const std = Math.sqrt(arr.reduce((sum, x) => sum + (x - mean) ** 2, 0) / arr.length);
    const variability = mean > 0 ? std / mean : 0;

    // Trend: compare first third to last third
    const third = Math.floor(arr.length / 3);
    if (third > 0) {
      const firstThird = arr.slice(0, third).reduce((a, b) => a + b, 0) / third;
      const lastThird = arr.slice(-third).reduce((a, b) => a + b, 0) / third;
      const change = (lastThird - firstThird) / firstThird;
      if (change > 0.1) return { peak, sustained, variability, trend: 'improving' as const };
      if (change < -0.1) return { peak, sustained, variability, trend: 'degrading' as const };
    }

    return { peak, sustained, variability, trend: 'stable' as const };
  }

  const dlStats = stats(dlSamples);
  const ulStats = stats(ulSamples);

  return {
    downloadPeakMbps: dlStats.peak,
    downloadSustainedMbps: dlStats.sustained,
    uploadPeakMbps: ulStats.peak,
    uploadSustainedMbps: ulStats.sustained,
    downloadVariability: dlStats.variability,
    uploadVariability: ulStats.variability,
    downloadTrend: dlStats.trend,
    uploadTrend: ulStats.trend
  };
}
```

**ui display:**

show bandwidth estimation card with:

- peak vs sustained comparison:
  ```
  Download: 850 Mbps peak / 720 Mbps sustained
  Upload: 420 Mbps peak / 380 Mbps sustained
  ```
- stability indicator: "Stable" / "Variable" based on variability coefficient
- trend arrow: ↑ improving / → stable / ↓ degrading
- variability percentage: "±12% variation"

---

## 12. network quality scoring (enhanced)

### 12.1 composite quality score

calculate an overall network quality score (0-100) and component subscores:

```ts
type NetworkQualityScore = {
  overall: number;           // 0-100 composite score
  components: {
    bandwidth: number;       // 0-100 based on download/upload speed
    latency: number;         // 0-100 based on unloaded latency
    stability: number;       // 0-100 based on jitter and variability
    reliability: number;     // 0-100 based on packet loss
  };
  grade: 'A+' | 'A' | 'B' | 'C' | 'D' | 'F';
  description: string;
};
```

**scoring algorithm:**

```ts
function calculateNetworkQualityScore(summary: Summary, bandwidth: BandwidthEstimate): NetworkQualityScore {
  // Bandwidth score (0-100)
  // 1000 Mbps = 100, 100 Mbps = 80, 25 Mbps = 50, 5 Mbps = 20
  const bwScore = Math.min(100,
    (Math.log10(Math.max(1, summary.downloadMbps)) / Math.log10(1000)) * 100
  );

  // Latency score (0-100)
  // <5ms = 100, 10ms = 90, 25ms = 70, 50ms = 50, 100ms = 20
  const latScore = Math.max(0, 100 - (summary.latencyUnloadedMs * 1.5));

  // Stability score (0-100)
  // Based on jitter and bandwidth variability
  const jitterPenalty = Math.min(50, summary.jitterMs * 3);
  const variabilityPenalty = Math.min(30, bandwidth.downloadVariability * 100);
  const stabScore = Math.max(0, 100 - jitterPenalty - variabilityPenalty);

  // Reliability score (0-100)
  // 0% loss = 100, 0.1% = 95, 1% = 70, 5% = 30
  const reliScore = Math.max(0, 100 - (summary.packetLossPercent * 15));

  // Weighted composite (bandwidth 35%, latency 25%, stability 20%, reliability 20%)
  const overall = Math.round(
    bwScore * 0.35 + latScore * 0.25 + stabScore * 0.20 + reliScore * 0.20
  );

  // Letter grade
  let grade: 'A+' | 'A' | 'B' | 'C' | 'D' | 'F';
  if (overall >= 95) grade = 'A+';
  else if (overall >= 85) grade = 'A';
  else if (overall >= 70) grade = 'B';
  else if (overall >= 55) grade = 'C';
  else if (overall >= 40) grade = 'D';
  else grade = 'F';

  // Description
  const descriptions = {
    'A+': 'Exceptional - Suitable for any application',
    'A': 'Excellent - Great for gaming, streaming, and video calls',
    'B': 'Good - Suitable for most online activities',
    'C': 'Fair - May experience occasional issues with demanding applications',
    'D': 'Poor - Expect frequent buffering and lag',
    'F': 'Very Poor - Connection issues likely for most activities'
  };

  return {
    overall,
    components: {
      bandwidth: Math.round(bwScore),
      latency: Math.round(latScore),
      stability: Math.round(stabScore),
      reliability: Math.round(reliScore)
    },
    grade,
    description: descriptions[grade]
  };
}
```

**ui display:**

show network quality score prominently:

- large circular gauge showing overall score (0-100)
- letter grade badge in center
- four component bars:
  - "Bandwidth: 85/100"
  - "Latency: 92/100"
  - "Stability: 78/100"
  - "Reliability: 95/100"
- description text below
- color coding: green (A+/A), blue (B), yellow (C), orange (D), red (F)

---

## 13. test confidence metrics

The browser and Go client expose the same five gates:

| gate | normal-mode requirement | deduction |
|---|---|---:|
| sample adequacy | 3 windows and 3 accepted loaded probes per enabled direction; at least 10 unloaded probes | 20 |
| variability | throughput CV below 30%; unloaded latency CV below 50% | 25 |
| loaded overlap | required probes prove continuous load | 25 |
| packet test | directional daemon report completed and reverse-ACK loss is measurable | 20 |
| timing accuracy | no imprecise timing fallback | 10 |

The result contains `sampleCount`, `coefficientOfVariation`, `loadedOverlap`,
`timingAccuracy`, and `packetTest` subrecords plus warnings. Scores of 80–100 are
high, 50–79 medium, and lower scores low. The UI renders these named gates rather
than the former placeholder outlier and generic connection-stability fields.

## 14. updated data model

### 14.1 enhanced result types

```ts
type EnhancedResults = {
  // Existing fields
  meta: Meta;
  locations: Location[];
  throughputSamples: ThroughputSample[];
  latencySamples: LatencySample[];
  packetLoss: PacketLossResult | null;
  summary: Summary;
  quality: NetworkQuality;
  startTime: number;
  endTime: number;

  // New fields
  extendedLocation: ExtendedLocation;
  timingBreakdown: TimingBreakdown[];
  lossPattern: LossPattern;
  dataChannelStats: DataChannelStats | null;
  bandwidthEstimate: BandwidthEstimate;
  networkQualityScore: NetworkQualityScore;
  testConfidence: TestConfidence;
};
```

### 14.2 timing sample extension

extend `ThroughputSample` to include timing breakdown:

```ts
type ThroughputSampleExtended = ThroughputSample & {
  timing?: TimingBreakdown;
};
```

---

## 15. bounded fixed-window selection

The giant profile table is removed. Each direction has only two planning
baselines—100 kB and 1 MB, three runs each—followed by three 1.5-second windows.

```text
target bytes = estimated bits/s / 8 × 0.250 s / concurrency
```

The chunk is rounded to 64 KiB, bounded to 100,000 bytes through 256 MiB, and
capped by the daemon. Browser concurrency is 1 below 100 Mbps, 2 at 100 Mbps,
4 at 500 Mbps, and at most 6 at 2 Gbps and above. Streaming upload bodies use
64 KiB chunks; non-streaming upload falls back to a reusable 8 MiB payload.
Non-streaming download is capped at 100 MB.

Workers repeat complete requests for the duration of the window. The window does
not count partial or unverifiable requests, and high rates scale through
concurrency rather than multi-gigabyte allocations.

## 16. packet loss error handling

### 16.1 ICE connection failure detection

the packet loss test monitors ICE connection state to detect failures:

```ts
pc.oniceconnectionstatechange = () => {
  const state = pc.iceConnectionState;
  if (state === 'failed') {
    reject(new Error('ICE connection failed'));
  } else if (state === 'disconnected') {
    // Give 2 seconds to recover before failing
    setTimeout(() => {
      if (pc.iceConnectionState === 'disconnected' || pc.iceConnectionState === 'failed') {
        reject(new Error('ICE connection disconnected'));
      }
    }, 2000);
  }
};
```

### 16.2 error types

the packet loss test can fail with the following error types:

| Error | Cause |
|-------|-------|
| `ICE connection timeout` | Connection setup took longer than 15 seconds |
| `ICE connection failed` | ICE negotiation failed (firewall, network issue) |
| `ICE connection disconnected` | Connection dropped during setup |
| `ICE gathering timeout` | ICE candidate gathering took longer than 10 seconds |
| `Data channel error` | WebRTC data channel failed to open |
| `Server rejected connection` | `/api/packet-test/offer` returned error |
| `TURN server not configured` | `/api/turn/credentials` returned 4xx/5xx |

### 16.3 unavailable result type

when an error occurs, return an unavailable result:

```ts
type PacketLossResultUnavailable = {
  sent: number;
  received: number;
  lossPercent: null;
  transactionLossPercent: null;
  forwardLossPercent: null;
  reverseAcknowledgementLossPercent: null;
  rttStatsMs: { min: 0; median: 0; p90: 0 };
  jitterMs: 0;
  unavailable: true;
  reason: string;
};
```

### 16.4 UI display for errors

when packet loss test is unavailable, display error state:

**main value:**
- show styled N/A marker (`<span class="na-marker"></span>`)
- add `error` class to value element

**badge:**
- show `Error` instead of `received/sent`

**detail text:**
- show `Unable to perform measurement: <reason>`
- examples:
  - `Unable to perform measurement: ICE connection timeout`
  - `Unable to perform measurement: ICE connection failed`
  - `Unable to perform measurement: TURN server not configured`

**RTT stats:**
- show N/A markers (not shimmer placeholders - data will never load)

**css for N/A marker:**

```css
.na-marker {
  display: inline-block;
  padding: 0.15em 0.5em;
  background-color: var(--color-text-tertiary);
  border-radius: 4px;
  color: var(--color-bg-primary);
  font-weight: 600;
  font-size: 0.85em;
  letter-spacing: 0.02em;
  vertical-align: middle;
  opacity: 0.7;
}

.na-marker::before {
  content: 'N/A';
}

.metric-value.error {
  color: var(--color-danger, #dc3545);
}
```

**implementation:**

```ts
function updatePacketLossDetails(packetLoss: PacketLossResult) {
  if (packetLoss.unavailable) {
    const errorMsg = `Unable to perform measurement: ${packetLoss.reason || 'Unknown error'}`;
    const naMarker = '<span class="na-marker"></span>';

    elements.packetLossValue.innerHTML = naMarker;
    elements.packetLossValue.classList.add('error');
    elements.packetLossBadge.textContent = 'Error';
    elements.packetLossDetail.textContent = errorMsg;
    elements.packetsReceived.textContent = errorMsg;

    // Show N/A markers for RTT stats (not shimmer - data will never load)
    elements.rttMin.innerHTML = naMarker;
    elements.rttMedian.innerHTML = naMarker;
    elements.rttP90.innerHTML = naMarker;
    elements.rttJitter.innerHTML = naMarker;
    return;
  }

  // Clear error state if previously set
  elements.packetLossValue.classList.remove('error');

  // ... normal display logic
}
```

### 16.5 connection issue detection

detect connection failures vs actual packet loss by analyzing response patterns:

```ts
// No responses at all - connection failed
if (received === 0) {
  return { unavailable: true, reason: 'No responses received - connection failed' };
}

// High loss with pattern analysis
if (lossPercent > 10) {
  const lateAckPercent = calculateLateAckPercent(acks, sent);
  const earlyAckPercent = calculateEarlyAckPercent(acks, sent);

  // Early packets succeeded but late packets failed = connection died
  if (earlyAckPercent > 80 && lateAckPercent < 50) {
    return { unavailable: true, reason: `Connection died mid-test - last response at packet ${maxAckedSeq}/${sent}` };
  }

  // Very high loss throughout = unstable connection
  if (lossPercent > 50) {
    return { unavailable: true, reason: `Connection unstable - received only ${received}/${sent} responses` };
  }
}
```

this prevents misleading packet loss percentages when the issue is connection failure rather than network quality.

---

## 17. progress callbacks

### 17.1 callback signatures

progress callbacks use consistent integer counts (not floats):

**download/upload progress:**
```ts
type ProgressCallback = (
  profile: string,      // e.g., '100kB', '1MB', '10MB'
  run: number,          // current run (1-indexed)
  totalRuns: number,    // total runs for this profile
  sample: ThroughputSample,
  totalComplete: number, // total samples completed so far
  expectedTotal: number  // expected total samples
) => void;
```

**latency progress:**
```ts
type LatencyProgressCallback = (
  phase: 'unloaded' | 'download' | 'upload',
  current: number,      // current probe count (1-indexed)
  total: number,        // total probes for this phase
  sample: LatencySample
) => void;
```

### 17.2 progress phases

progress uses different denominators based on test phase:

| Phase | Denominator | Description |
|-------|-------------|-------------|
| Baseline (100kB + 1MB) | baselineRuns (18) | Fixed count: 10 + 8 runs |
| Larger profiles | expectedTotal | Baseline + selected profile runs, minus skipped batches |

**expectedTotal adjustment:**
when batches are skipped due to time budget, `expectedTotal` is decremented:

```ts
if (estimatedBatchTime > remainingMs) {
  expectedTotal -= runs;  // Adjust for skipped batch
  continue;
}
```

this ensures progress reaches 100% even when batches are skipped.

---

## 18. shared URL encoding

### 18.1 loss type encoding fix

when encoding share URLs, if loss pattern type is unknown but there's actual packet loss, default to 'random':

**encoding:**
```ts
let lossType = lossTypes[lp?.type] ?? 0;  // 0 = 'none'
if (lossType === 0 && state.packetLoss?.lossPercent > 0.5) {
  lossType = 1;  // 'random' - we know there was loss but pattern unknown
}
```

**decoding:**
```ts
let resolvedLossType = lossTypes[lossType] || 'none';
if (resolvedLossType === 'none' && packetLoss.lossPercent > 0.5) {
  resolvedLossType = 'random';  // Infer loss type from actual loss data
}
```

this prevents the loss type badge from showing "No Loss" when there's clearly packet loss in the decoded results.

### 18.2 shared results button fix

when viewing shared results, update the start button text without destroying inner elements:

**correct approach:**
```ts
if (elements.startButton) {
  const span = elements.startButton.querySelector('span');
  if (span) {
    span.textContent = 'Run New Test';  // Update span, not button
  } else {
    elements.startButton.textContent = 'Run New Test';
  }
  elements.startButton.disabled = false;
}
```

**incorrect approach (breaks button):**
```ts
// DON'T DO THIS - destroys inner <span> element
elements.startButton.textContent = 'Run New Test';
```

