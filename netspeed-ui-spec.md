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
     - mark that card as “Test failed” with a tooltip.
     - still compute partial summary from available data.
   - if TURN / WebRTC fails:
     - show “Packet loss test unavailable” placeholder.
     - do not block other metrics.

