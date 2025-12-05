# netspeed UI - speedtest frontend spec (http + TURN)

this document specifies the **browser-side behavior and ui** for a speedtest app
that talks to a backend exposing these endpoints:

- `GET /meta`
- `GET /__down`
- `POST /__up`
- `GET /locations`
- `GET /api/turn/credentials`
- `POST /api/packet-test/offer`
- (optional) `POST /api/packet-test/report`

this is a **frontend-only** spec: it defines what requests the browser makes,
what responses it expects, how tests are orchestrated, and how results are
displayed. backend implementation details (turn server config, hmac secrets,
etc.) are intentionally out of scope.

---

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
  "timezone": "America/Los_Angeles"
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

**request**

- method: `GET`
- path: `/__down`
- query parameters:

  - `bytes` (string int, optional)
    - number of bytes to download.
    - `0` or omitted → latency-only probe.
  - `profile` (string, optional)
    - name of download profile: `100k`, `1M`, `10M`, `25M`, `100M`.
  - `run` (string int, optional)
    - run index within a profile.
  - `phase` (string, optional)
    - `unloaded`, `download`, `upload` (for latency probes).

**response**

- status: `200`
- headers:
  - `Content-Type: application/octet-stream`
  - `Content-Length: <bytes>` (may be `0`)
  - other headers are ignored by the frontend.
- body: exactly `bytes` bytes of opaque data.

**frontend behavior**

- measure round-trip time using `performance.now()`:

  ```ts
  const start = performance.now();
  const res = await fetch(url, { cache: 'no-store' });
  const reader = res.body!.getReader();
  let received = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    received += value.byteLength;
  }
  const end = performance.now();
  const durationMs = end - start;
  ```

- for `bytes > 0`, compute throughput:

  ```ts
  const mbps = (received * 8) / (durationMs / 1000) / 1e6;
  ```

- for `bytes == 0`, use `durationMs` as a latency sample.

---

#### 1.1.3 `POST /__up` — upload

**request**

- method: `POST`
- path: `/__up`
- query parameters:

  - `profile` (string, optional: `100k`, `1M`, `10M`, `25M`, `50M`)
  - `run` (string int, optional)
  - `phase` (string, optional: `upload` for latency-under-load probes)

- headers:
  - `Content-Type: application/octet-stream` (recommended)
- body:
  - binary payload of the desired size.

**response**

- status: `200` (or `204`)
- body: ignored by frontend.

**frontend behavior**

- create a reusable `ArrayBuffer` per upload profile size.
- send it as the body while measuring duration the same way as download.
- compute mbps from payload size and duration.

---

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
  "testProfile": "loss-basic"
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
    testProfile: 'loss-basic'
  })
});

const { sdp, type, testId } = await res.json();
await pc.setRemoteDescription(new RTCSessionDescription({ sdp, type }));

// store testId alongside results (optional)
```

the frontend assumes the server will create a corresponding peer connection and
respond with a valid WebRTC answer.

---

#### 1.2.3 `POST /api/packet-test/report` (optional)

**purpose:** report the final packet-loss and summary metrics to the backend.

**request**

- method: `POST`
- path: `/api/packet-test/report`
- headers: `Content-Type: application/json`
- body (example):

```json
{
  "testId": "c65b0b1d-6f7f-4a9a-9f2b-7c9d3c5f0c3a",
  "lossResult": {
    "sent": 1000,
    "received": 995,
    "lossPercent": 0.5,
    "rttStatsMs": { "min": 15, "median": 20, "p90": 30 },
    "jitterMs": 3.2
  },
  "summary": {
    "downloadMbps": 778.1,
    "uploadMbps": 757.2,
    "latencyUnloadedMs": 6.0,
    "latencyDownloadMs": 15.0,
    "latencyUploadMs": 21.0,
    "jitterMs": 1.2,
    "packetLossPercent": 0.5
  },
  "meta": {
    "asn": 13254,
    "colo": "PDX"
  }
}
```

**response**

- status: `200` or `204`
- body: ignored by frontend.

frontend must treat this endpoint as optional; if the request fails, tests and
ui still complete locally.

---

## 2. measurement pipeline (frontend behavior)

frontend runs four kinds of tests in a defined sequence:

1. download speed (`/__down`)
2. upload speed (`/__up`)
3. latency (`/__down?bytes=0`)
4. packet loss (TURN + WebRTC)

### 2.1 download speed tests

**profiles & sizes:**

- `100k` → `100 * 1024` bytes
- `1M`   → `1 * 1024 * 1024`
- `10M`  → `10 * 1024 * 1024`
- `25M`  → `25 * 1024 * 1024`
- `100M` → `100 * 1024 * 1024`

**per profile:**

- `runs` per profile (e.g. 3–10).
- for each run:

  ```ts
  const url = `/__down?bytes=${sizeBytes}&profile=${profile}&run=${runIndex}`;
  const start = performance.now();
  const res = await fetch(url, { cache: 'no-store' });
  const reader = res.body!.getReader();
  let received = 0;

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    received += value.byteLength;
  }

  const end = performance.now();
  const durationMs = end - start;
  const mbps = (received * 8) / (durationMs / 1000) / 1e6;
  ```

- store each result as a `ThroughputSample`.

---

### 2.2 upload speed tests

**profiles & sizes:**

- `100k`, `1M`, `10M`, `25M`, `50M` (same size rule as download).

**per profile:**

- prepare payload once:

  ```ts
  const payload = new Uint8Array(sizeBytes); // zero-filled is fine
  ```

- per run:

  ```ts
  const url = `/__up?profile=${profile}&run=${runIndex}`;
  const start = performance.now();
  const res = await fetch(url, {
    method: 'POST',
    body: payload,
    headers: { 'Content-Type': 'application/octet-stream' }
  });
  await res.arrayBuffer();
  const end = performance.now();
  const durationMs = end - start;
  const mbps = (sizeBytes * 8) / (durationMs / 1000) / 1e6;
  ```

- store as `ThroughputSample` with `direction='upload'`.

---

### 2.3 latency tests

latency tests call `GET /__down?bytes=0` and treat the duration as rtt.

**phases:**

1. **unloaded latency**
   - 20 sequential probes:

     ```ts
     const url = `/__down?bytes=0&phase=unloaded&seq=${i}`;
     ```

   - measure `durationMs` per request; store as `LatencySample` with `phase='unloaded'`.

2. **latency during download**
   - while a medium/large download profile is running (e.g. `10M` or `25M`):
     - schedule 5 latency probes using `phase='download'`.

       ```ts
       const url = `/__down?bytes=0&phase=download&seq=${i}`;
       ```

3. **latency during upload**
   - same approach, but overlapping with an active upload test:

     ```ts
     const url = `/__down?bytes=0&phase=upload&seq=${i}`;
     ```

frontend coordinates these phases to ensure overlapping load and latency probes.

---

### 2.4 packet loss test (TURN + WebRTC)

profile: `"loss-basic"`.

**steps:**

1. call `GET /api/turn/credentials` and configure `RTCPeerConnection`.
2. create `RTCDataChannel` labeled `"packet-loss"`:

   ```ts
   const dc = pc.createDataChannel('packet-loss', {
     ordered: true,
     maxRetransmits: 0
   });
   ```

3. perform SDP offer/answer through `POST /api/packet-test/offer` (see 1.2.2).
4. once the data channel `open` event fires, run the packet test:

   ```ts
   const N = 1000;
   const intervalMs = 10;
   let seq = 0;
   const acks = new Set<number>();

   dc.onmessage = (event) => {
     const msg = JSON.parse(event.data);
     if (typeof msg.ack === 'number') {
       acks.add(msg.ack);
     }
   };

   const timer = setInterval(() => {
     if (seq >= N) {
       clearInterval(timer);
       return;
     }

     const msg = {
       seq,
       sentAt: Date.now(),
       size: 1200
     };
     dc.send(JSON.stringify(msg));
     seq++;
   }, intervalMs);
   ```

5. after all packets are sent, wait an additional `extraWaitMs` (e.g. `3000`) for late acks.
6. compute loss statistics:

   ```ts
   const sent = N;
   const received = acks.size;
   const lossPercent = (sent - received) / sent * 100;
   ```

7. optionally call `pc.getStats()`:

   - derive RTT and jitter (implementation-specific).
8. assemble `PacketLossResult` and store it in app state.
9. optionally send to `/api/packet-test/report`.

---

## 3. frontend data model

```ts
type LatencyPhase = 'unloaded' | 'download' | 'upload';

type LatencySample = {
  ts: number;          // ms since epoch
  rttMs: number;       // measured round-trip time
  phase: LatencyPhase;
};

type ThroughputDirection = 'download' | 'upload';

type ThroughputProfile =
  | '100k'
  | '1M'
  | '10M'
  | '25M'
  | '50M'
  | '100M';

type ThroughputSample = {
  ts: number;
  direction: ThroughputDirection;
  sizeBytes: number;
  durationMs: number;
  mbps: number;
  profile: ThroughputProfile;
  runIndex: number;
};

type PacketLossResult = {
  sent: number;
  received: number;
  lossPercent: number;
  rttStatsMs: {
    min: number;
    median: number;
    p90: number;
  };
  jitterMs: number;
  testId?: string;             // from /api/packet-test/offer
};

type Summary = {
  downloadMbps: number;
  uploadMbps: number;
  latencyUnloadedMs: number;
  latencyDownloadMs: number;
  latencyUploadMs: number;
  jitterMs: number;
  packetLossPercent: number;
};

type NetworkQualityGrade = 'Great' | 'Good' | 'Okay' | 'Poor';

type NetworkQuality = {
  videoStreaming: NetworkQualityGrade;
  gaming: NetworkQualityGrade;
  videoChatting: NetworkQualityGrade;
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
};

type Location = {
  iata: string;
  lat: number;
  lon: number;
  cca2: string;
  region: string;
  city: string;
};
```

---

## 4. summary metrics & grading

### 4.1 summary calculations

computed from samples collected in sections 2.1–2.4.

```ts
function buildSummary(
  throughput: ThroughputSample[],
  latency: LatencySample[],
  loss: PacketLossResult | null
): Summary {
  // helper functions p50/p90 omitted here
  const dl = throughput.filter(s => s.direction === 'download').map(s => s.mbps);
  const ul = throughput.filter(s => s.direction === 'upload').map(s => s.mbps);

  const latUnloaded = latency.filter(l => l.phase === 'unloaded').map(l => l.rttMs);
  const latDownload = latency.filter(l => l.phase === 'download').map(l => l.rttMs);
  const latUpload   = latency.filter(l => l.phase === 'upload').map(l => l.rttMs);

  return {
    downloadMbps: p90(dl),
    uploadMbps: p90(ul),
    latencyUnloadedMs: p50(latUnloaded),
    latencyDownloadMs: p90(latDownload),
    latencyUploadMs: p90(latUpload),
    jitterMs: p90(latUnloaded) - p50(latUnloaded),
    packetLossPercent: loss ? loss.lossPercent : 0
  };
}
```

`p50` = median, `p90` = 90th percentile.

### 4.2 network quality grading (example)

```ts
function gradeForStreaming(s: Summary): NetworkQualityGrade {
  const { downloadMbps, latencyUnloadedMs, jitterMs, packetLossPercent } = s;

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

### 13.1 measurement quality assessment

evaluate the confidence/reliability of test results:

```ts
type TestConfidence = {
  overall: 'high' | 'medium' | 'low';
  overallScore: number;          // 0-100

  metrics: {
    sampleCount: {
      download: number;
      upload: number;
      latency: number;
      adequate: boolean;
    };

    coefficientOfVariation: {
      download: number;          // std/mean as percentage
      upload: number;
      latency: number;
      acceptable: boolean;       // <30% is acceptable
    };

    outlierRate: {
      download: number;          // percentage of samples excluded
      upload: number;
      latency: number;
      acceptable: boolean;       // <20% is acceptable
    };

    timingAccuracy: {
      resourceTimingUsed: boolean;
      serverTimingUsed: boolean;
      fallbackCount: number;
      accurate: boolean;
    };

    connectionStability: {
      abortedTests: number;
      retriedTests: number;
      packetTestCompleted: boolean;
      stable: boolean;
    };
  };

  warnings: string[];
};
```

**calculation:**

```ts
function assessTestConfidence(
  samples: ThroughputSample[],
  latency: LatencySample[],
  packetLoss: PacketLossResult | null,
  timingStats: { resourceTiming: number; serverTiming: number; fallback: number }
): TestConfidence {
  const warnings: string[] = [];

  // Sample counts
  const dlCount = samples.filter(s => s.direction === 'download').length;
  const ulCount = samples.filter(s => s.direction === 'upload').length;
  const latCount = latency.filter(s => s.phase === 'unloaded').length;
  const sampleAdequate = dlCount >= 20 && ulCount >= 15 && latCount >= 10;
  if (!sampleAdequate) warnings.push('Insufficient samples for high confidence');

  // Coefficient of variation
  function cv(arr: number[]): number {
    if (arr.length < 2) return 0;
    const mean = arr.reduce((a, b) => a + b, 0) / arr.length;
    const std = Math.sqrt(arr.reduce((sum, x) => sum + (x - mean) ** 2, 0) / arr.length);
    return (std / mean) * 100;
  }

  const dlCV = cv(samples.filter(s => s.direction === 'download').map(s => s.mbps));
  const ulCV = cv(samples.filter(s => s.direction === 'upload').map(s => s.mbps));
  const latCV = cv(latency.filter(s => s.phase === 'unloaded').map(s => s.rttMs));
  const cvAcceptable = dlCV < 30 && ulCV < 30 && latCV < 50;
  if (!cvAcceptable) warnings.push('High variability in measurements');

  // Outlier rate (samples that were filtered out)
  // This would need to be tracked during test execution
  const outlierAcceptable = true; // placeholder

  // Timing accuracy
  const totalRequests = timingStats.resourceTiming + timingStats.serverTiming + timingStats.fallback;
  const accurateTimingPercent = totalRequests > 0
    ? ((timingStats.resourceTiming + timingStats.serverTiming) / totalRequests) * 100
    : 0;
  const timingAccurate = accurateTimingPercent > 80;
  if (!timingAccurate) warnings.push('Timing API fallbacks may reduce accuracy');

  // Connection stability
  const connectionStable = packetLoss !== null && !packetLoss.unavailable;
  if (!connectionStable) warnings.push('Packet loss test incomplete');

  // Overall score (0-100)
  let score = 100;
  if (!sampleAdequate) score -= 20;
  if (!cvAcceptable) score -= 25;
  if (!timingAccurate) score -= 15;
  if (!connectionStable) score -= 15;

  let overall: 'high' | 'medium' | 'low';
  if (score >= 80) overall = 'high';
  else if (score >= 50) overall = 'medium';
  else overall = 'low';

  return {
    overall,
    overallScore: Math.max(0, score),
    metrics: {
      sampleCount: { download: dlCount, upload: ulCount, latency: latCount, adequate: sampleAdequate },
      coefficientOfVariation: { download: dlCV, upload: ulCV, latency: latCV, acceptable: cvAcceptable },
      outlierRate: { download: 0, upload: 0, latency: 0, acceptable: outlierAcceptable },
      timingAccuracy: {
        resourceTimingUsed: timingStats.resourceTiming > 0,
        serverTimingUsed: timingStats.serverTiming > 0,
        fallbackCount: timingStats.fallback,
        accurate: timingAccurate
      },
      connectionStability: {
        abortedTests: 0,
        retriedTests: 0,
        packetTestCompleted: connectionStable,
        stable: connectionStable
      }
    },
    warnings
  };
}
```

**ui display:**

show test confidence section:

- confidence badge: "High Confidence" (green) / "Medium Confidence" (yellow) / "Low Confidence" (red)
- expandable details:
  - "Samples: ✓ Download (31), ✓ Upload (25), ✓ Latency (18)"
  - "Variability: ✓ Download ±8%, ✓ Upload ±12%, ✓ Latency ±15%"
  - "Timing: ✓ Resource Timing API used (95% of requests)"
  - "Connection: ✓ Stable throughout test"
- warnings list (if any):
  - "⚠ High variability in measurements"
  - "⚠ Some timing fallbacks used"

---

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

## 15. test profiles and adaptive selection

### 15.1 all available download profiles

| Profile | Size | Runs | Notes |
|---------|------|------|-------|
| 100kB   | 100,000 bytes | 10 | baseline (always included) |
| 1MB     | 1,000,000 bytes | 8 | baseline (always included) |
| 10MB    | 10,000,000 bytes | 6 | |
| 25MB    | 25,000,000 bytes | 4 | |
| 100MB   | 100,000,000 bytes | 3 | |
| 250MB   | 250,000,000 bytes | 2 | |
| 500MB   | 500,000,000 bytes | 2 | 1s at 4 Gbps |
| 1GB     | 1,000,000,000 bytes | 2 | 1s at 8 Gbps |
| 2GB     | 2,000,000,000 bytes | 2 | 1s at 16 Gbps |
| 5GB     | 5,000,000,000 bytes | 2 | 1s at 40 Gbps |
| 12GB    | 12,000,000,000 bytes | 2 | 1s at ~100 Gbps |
| 50GB    | 50,000,000,000 bytes | 2 | 1s at 400 Gbps |
| 100GB   | 100,000,000,000 bytes | 2 | 1s at 800 Gbps |
| 125GB   | 125,000,000,000 bytes | 2 | 1s at 1 Tbps |

### 15.2 all available upload profiles

| Profile | Size | Runs | Notes |
|---------|------|------|-------|
| 100kB   | 100,000 bytes | 8 | baseline (always included) |
| 1MB     | 1,000,000 bytes | 6 | baseline (always included) |
| 10MB    | 10,000,000 bytes | 4 | |
| 25MB    | 25,000,000 bytes | 4 | |
| 50MB    | 50,000,000 bytes | 3 | |
| 100MB   | 100,000,000 bytes | 2 | |
| 250MB   | 250,000,000 bytes | 2 | 1s at 2 Gbps |
| 500MB   | 500,000,000 bytes | 2 | 1s at 4 Gbps |
| 1GB     | 1,000,000,000 bytes | 2 | 1s at 8 Gbps |
| 2GB     | 2,000,000,000 bytes | 2 | 1s at 16 Gbps |
| 5GB     | 5,000,000,000 bytes | 2 | 1s at 40 Gbps |
| 12GB    | 12,000,000,000 bytes | 2 | 1s at ~100 Gbps |
| 50GB    | 50,000,000,000 bytes | 2 | 1s at 400 Gbps |
| 100GB   | 100,000,000,000 bytes | 2 | 1s at 800 Gbps |
| 125GB   | 125,000,000,000 bytes | 2 | 1s at 1 Tbps |

**Note:** Sizes use decimal (kB/MB/GB) notation: 1 kB = 1,000 bytes, 1 MB = 1,000,000 bytes, 1 GB = 1,000,000,000 bytes.

### 15.3 adaptive profile selection

profiles are selected dynamically based on estimated connection speed. the algorithm uses linear time-based scaling:

**formula:**
```
estimatedTime = (bytes × 8) / (speedMbps × 1,000,000)
include profile if estimatedTime ≤ 4 seconds
```

**selection process:**

1. **estimation phase:** run 4 tests with 1MB profile to estimate speed
2. **profile selection:** include profiles where estimated transfer time ≤ 4 seconds
3. **test execution:** run remaining tests with selected profiles

**baseline profiles (always included):**
- 100kB and 1MB are always included regardless of speed

**examples at various speeds:**

| Speed | Download Profiles | Upload Profiles |
|-------|-------------------|-----------------|
| 128 Kbps | 100kB, 1MB | 100kB, 1MB |
| 5 Mbps | 100kB, 1MB | 100kB, 1MB |
| 40 Mbps | 100kB, 1MB, 10MB | 100kB, 1MB |
| 200 Mbps | 100kB, 1MB, ... 100MB | 100kB, 1MB, ... 100MB |
| 1 Gbps | 100kB, 1MB, ... 500MB | 100kB, 1MB, ... 500MB |
| 10 Gbps | 100kB, 1MB, ... 5GB | 100kB, 1MB, ... 5GB |
| 100 Gbps | 100kB, 1MB, ... 50GB | 100kB, 1MB, ... 50GB |
| 400 Gbps | 100kB, 1MB, ... 125GB | 100kB, 1MB, ... 125GB |
| 800 Gbps | all profiles | all profiles |
| 1 Tbps | all profiles | all profiles |

**implementation:**

```ts
function estimateTransferTime(bytes: number, speedMbps: number): number {
  if (speedMbps <= 0) return Infinity;
  return (bytes * 8) / (speedMbps * 1e6);
}

function selectProfiles(estimatedSpeedMbps: number, allProfiles: ProfileMap): ProfileMap {
  // Always include baseline
  const profiles = {
    '100kB': allProfiles['100kB'],
    '1MB': allProfiles['1MB']
  };

  // Add larger profiles based on estimated transfer time
  for (const [name, profile] of Object.entries(allProfiles)) {
    if (name === '100kB' || name === '1MB') continue;
    const estimatedSeconds = estimateTransferTime(profile.bytes, estimatedSpeedMbps);
    if (estimatedSeconds <= 4) {
      profiles[name] = profile;
    }
  }

  return profiles;
}
```

this ensures:
- slow connections (128 Kbps) only run small tests that complete quickly
- fast connections (1+ Gbps) run larger tests for accurate measurements
- extremely fast connections (10+ Gbps) use GB-sized tests
- linear scaling works across the full range from 128 Kbps to 1 Tbps

---

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
  sent: 0;
  received: 0;
  lossPercent: 0;
  rttStatsMs: { min: 0; median: 0; p90: 0 };
  jitterMs: 0;
  unavailable: true;
  reason: string;  // human-readable error message
};
```

### 16.4 UI display for errors

when packet loss test is unavailable, display error state:

**main value:**
- show red circle with exclamation point icon (`<span class="error-icon"></span>`)
- add `error` class to value element for red color styling

**badge:**
- show `Error` instead of `received/sent`

**detail text:**
- show `Unable to perform measurement: <reason>`
- examples:
  - `Unable to perform measurement: ICE connection timeout`
  - `Unable to perform measurement: ICE connection failed`
  - `Unable to perform measurement: TURN server not configured`

**RTT stats:**
- show shimmer placeholder spans (animated loading indicator)

**css for error icon:**

```css
.error-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1.5em;
  height: 1.5em;
  background-color: var(--color-danger, #dc3545);
  border-radius: 50%;
  color: white;
  font-weight: bold;
}

.error-icon::before {
  content: '!';
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

    elements.packetLossValue.innerHTML = '<span class="error-icon"></span>';
    elements.packetLossValue.classList.add('error');
    elements.packetLossBadge.textContent = 'Error';
    elements.packetLossDetail.textContent = errorMsg;
    elements.packetsReceived.textContent = errorMsg;

    // Show shimmer placeholders for RTT stats
    const ph = '<span class="placeholder"></span>';
    elements.rttMin.innerHTML = ph;
    elements.rttMedian.innerHTML = ph;
    elements.rttP90.innerHTML = ph;
    elements.rttJitter.innerHTML = ph;
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

