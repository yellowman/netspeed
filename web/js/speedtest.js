/**
 * Speedtest measurement module
 * Handles download, upload, latency, and packet loss tests
 */

const SpeedTest = (function() {
    'use strict';

    // Phase 2 uses small verified baselines only. Headline throughput comes
    // from repeated bounded requests inside fixed-duration windows; no profile
    // can request or allocate gigabytes merely because the baseline was fast.
    const ALL_DOWNLOAD_PROFILES = {
        '100kB': { bytes: 100 * 1000, runs: 3 },
        '1MB':   { bytes: 1 * 1000 * 1000, runs: 3 }
    };

    const ALL_UPLOAD_PROFILES = {
        '100kB': { bytes: 100 * 1000, runs: 3 },
        '1MB':   { bytes: 1 * 1000 * 1000, runs: 3 }
    };

    const LEGACY_SERVER_TRANSFER_LIMIT_BYTES = 100 * 1000 * 1000;
    const WEB_DOWNLOAD_FALLBACK_MEMORY_LIMIT_BYTES = 100 * 1000 * 1000;
    const WEB_UPLOAD_FALLBACK_CHUNK_BYTES = 8 * 1024 * 1024;
    const MIN_WINDOW_CHUNK_BYTES = 100 * 1000;
    const MAX_WINDOW_CHUNK_BYTES = 256 * 1024 * 1024;
    const TARGET_REQUEST_DURATION_MS = 250;
    const WINDOW_DURATION_MS = 1500;
    const WINDOW_COUNT = 3;
    const LOADED_WINDOW_INDEX = 1;
    const MAX_BROWSER_CONCURRENCY = 6;

    const REQUIRED_MEASUREMENT_PROTOCOL_VERSION = 2;
    const REQUIRED_UPLOAD_RECEIPT_VERSION = 1;
    const REQUIRED_PACKET_LOSS_FRAME_VERSION = 1;
    const PACKET_FRAME_SIZE = 1200;
    const PACKET_FRAME_HEADER_SIZE = 32;
    const PACKET_FRAME_VERSION = 1;
    const PACKET_FRAME_PROBE = 1;
    const PACKET_FRAME_ACK = 2;
    const PACKET_FRAME_MAGIC = [0x4e, 0x53, 0x50, 0x4c]; // NSPL

    const MAX_UPLOAD_RECEIPT_BYTES = 64 * 1024;
    const MAX_PACKET_REPORT_BYTES = 64 * 1024;
    const MAX_TURN_CREDENTIAL_BYTES = 64 * 1024;
    const MAX_SIGNALING_BODY_BYTES = 1024 * 1024;
    const MAX_META_BODY_BYTES = 1024 * 1024;
    const MAX_LOCATIONS_BODY_BYTES = 1024 * 1024;

    let serverMaxTransferBytes = LEGACY_SERVER_TRANSFER_LIMIT_BYTES;
    let serverMaxConcurrentTransfersPerClient = 24;
    let measurementProtocolVersion = 0;
    let uploadReceiptVersion = 0;
    let packetLossFrameVersion = 0;
    let requestStreamingSupport;

    // Active plans are exposed for diagnostics. They contain bounded chunks and
    // concurrency rather than the former giant profile table.
    const DOWNLOAD_PROFILES = { ...ALL_DOWNLOAD_PROFILES };
    const UPLOAD_PROFILES = { ...ALL_UPLOAD_PROFILES };

    const CONFIG = {
        latencyProbes: 20,
        loadedLatencyProbes: 5,
        packetLossPackets: 1000,
        packetLossInterval: 10,
        packetLossExtraWait: 3000
    };


    function accessToken() {
        if (typeof globalThis === 'undefined') return '';
        const value = globalThis.NETSPEED_CONFIG?.accessToken;
        return typeof value === 'string' ? value.trim() : '';
    }

    function browserPageURL() {
        if (typeof window !== 'undefined' && window.location) {
            if (typeof window.location.href === 'string' && window.location.href) return window.location.href;
            if (typeof window.location.origin === 'string' && window.location.origin) return `${window.location.origin}/`;
        }
        return 'http://localhost/';
    }

    function configuredAPIBaseURL() {
        if (typeof globalThis === 'undefined') return '';
        const configured = globalThis.NETSPEED_CONFIG?.apiBaseUrl;
        if (configured === undefined || configured === null || String(configured).trim() === '') return '';

        const parsed = new URL(String(configured).trim(), browserPageURL());
        if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
            throw new Error(`NETSPEED_CONFIG.apiBaseUrl must use http or https: ${parsed.protocol}`);
        }
        if (parsed.username || parsed.password || parsed.search || parsed.hash) {
            throw new Error('NETSPEED_CONFIG.apiBaseUrl cannot contain credentials, query parameters, or a fragment');
        }
        if (!parsed.pathname.endsWith('/')) parsed.pathname += '/';
        return parsed.href;
    }

    function apiURL(path) {
        const relativePath = String(path).replace(/^\/+/, '');
        const base = configuredAPIBaseURL();
        if (!base) return `/${relativePath}`;
        return new URL(relativePath, base).href;
    }

    function requestCredentialsMode() {
        if (typeof globalThis === 'undefined') return 'same-origin';
        const configured = globalThis.NETSPEED_CONFIG?.credentials;
        if (configured === 'omit' || configured === 'same-origin' || configured === 'include') return configured;
        if (globalThis.NETSPEED_CONFIG?.includeCredentials === true) return 'include';
        return 'same-origin';
    }

    function requestOptions(options = {}) {
        return { ...options, credentials: requestCredentialsMode() };
    }

    function xhrCanHonorCredentials(url) {
        const mode = requestCredentialsMode();
        if (mode !== 'omit') return true;
        // XHR cannot suppress cookies on a same-origin request. In that one
        // case use Fetch so credentials:'omit' remains truthful, even though a
        // non-streaming browser must then mark upload-load timing imprecise.
        return new URL(url, browserPageURL()).origin !== new URL(browserPageURL()).origin;
    }

    function authenticatedHeaders(base = {}) {
        const headers = { ...base };
        const token = accessToken();
        if (token) headers.Authorization = `Bearer ${token}`;
        return headers;
    }

    function supportsStreamingResponseBodies() {
        return typeof Response !== 'undefined' &&
            Response.prototype &&
            'body' in Response.prototype &&
            typeof ReadableStream !== 'undefined';
    }

    // Chromium requires duplex:'half' for request streams; other browsers may
    // not expose the duplex getter at all. This feature test avoids attempting
    // a streaming request where fetch would buffer or reject it.
    function supportsStreamingRequestBodies() {
        if (requestStreamingSupport !== undefined) return requestStreamingSupport;
        if (typeof ReadableStream === 'undefined' || typeof Request === 'undefined') {
            requestStreamingSupport = false;
            return requestStreamingSupport;
        }
        try {
            let duplexAccessed = false;
            const stream = new ReadableStream({ start(controller) { controller.close(); } });
            const request = new Request('http://localhost/', {
                method: 'POST',
                body: stream,
                get duplex() {
                    duplexAccessed = true;
                    return 'half';
                }
            });
            requestStreamingSupport = duplexAccessed && !request.headers.has('Content-Type');
        } catch (_) {
            requestStreamingSupport = false;
        }
        return requestStreamingSupport;
    }

    function maxDownloadBytes() {
        if (supportsStreamingResponseBodies()) return serverMaxTransferBytes;
        return Math.min(serverMaxTransferBytes, WEB_DOWNLOAD_FALLBACK_MEMORY_LIMIT_BYTES);
    }

    function maxUploadBytes() {
        return Math.min(serverMaxTransferBytes, WEB_UPLOAD_FALLBACK_CHUNK_BYTES);
    }

    function maxUploadWindowChunkBytes() {
        return Math.min(
            serverMaxTransferBytes,
            supportsStreamingRequestBodies() ? MAX_WINDOW_CHUNK_BYTES : WEB_UPLOAD_FALLBACK_CHUNK_BYTES
        );
    }

    function requiredSuccessfulRuns(total) {
        return total <= 1 ? total : Math.ceil(total / 2);
    }

    async function consumeResponseBody(response, expectedBytes) {
        if (!Number.isSafeInteger(expectedBytes) || expectedBytes < 0) {
            throw new Error(`Invalid expected response size: ${expectedBytes}`);
        }

        if (!response.body || typeof response.body.getReader !== 'function') {
            if (expectedBytes > WEB_DOWNLOAD_FALLBACK_MEMORY_LIMIT_BYTES) {
                throw new Error(`Streaming response bodies are unavailable; refusing to buffer ${expectedBytes} bytes`);
            }
            return (await response.arrayBuffer()).byteLength;
        }

        const reader = response.body.getReader();
        let received = 0;
        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            received += value.byteLength;
            if (received > expectedBytes) {
                await reader.cancel('response exceeded expected length');
                break;
            }
        }
        return received;
    }

    async function readJSONBody(response, maxBytes) {
        if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0) {
            throw new Error(`Invalid JSON response limit: ${maxBytes}`);
        }

        let bytes;
        if (!response.body || typeof response.body.getReader !== 'function') {
            const buffer = await response.arrayBuffer();
            if (buffer.byteLength > maxBytes) throw new Error(`JSON response exceeds ${maxBytes} bytes`);
            bytes = new Uint8Array(buffer);
        } else {
            const reader = response.body.getReader();
            const chunks = [];
            let received = 0;
            while (true) {
                const { done, value } = await reader.read();
                if (done) break;
                received += value.byteLength;
                if (received > maxBytes) {
                    await reader.cancel('JSON response exceeded limit');
                    throw new Error(`JSON response exceeds ${maxBytes} bytes`);
                }
                chunks.push(value);
            }
            bytes = new Uint8Array(received);
            let offset = 0;
            for (const chunk of chunks) {
                bytes.set(chunk, offset);
                offset += chunk.byteLength;
            }
        }

        const text = new TextDecoder().decode(bytes);
        return JSON.parse(text);
    }

    function estimateSpeed(samples) {
        return percentile(samples, 50);
    }

    // State
    let abortController = null;
    let isRunning = false;
    let isPaused = false;
    let timingFallbackCount = 0;
    let resourceTimingUsed = false;

    // Results storage
    let results = {
        meta: null,
        locations: [],
        throughputSamples: [],
        latencySamples: [],
        packetLoss: null,
        startTime: null,
        endTime: null,
        // New enhanced fields
        lossPattern: null,
        dataChannelStats: null,
        bandwidthEstimate: null,
        networkQualityScore: null,
        testConfidence: null
    };

    // Event callbacks
    let callbacks = {
        onProgress: null,
        onMetaReceived: null,
        onDownloadProgress: null,
        onUploadProgress: null,
        onLatencyProgress: null,
        onPacketLossProgress: null,
        onComplete: null,
        onError: null,
        onTimingWarning: null
    };

    /**
     * Set event callbacks
     */
    function setCallbacks(cbs) {
        Object.assign(callbacks, cbs);
    }

    /**
     * Fetch metadata from server
     */
    async function fetchMeta() {
        const response = await fetch(apiURL('/meta'), requestOptions({ cache: 'no-store', headers: authenticatedHeaders() }));
        if (!response.ok) throw new Error(`Failed to fetch metadata: HTTP ${response.status}`);
        const contentType = (response.headers.get('Content-Type') || '').toLowerCase();
        if (!contentType.startsWith('application/json')) {
            throw new Error(`Metadata returned unexpected content type: ${contentType || 'missing'}`);
        }
        return readJSONBody(response, MAX_META_BODY_BYTES);
    }

    /**
     * Fetch locations from server
     */
    async function fetchLocations() {
        const response = await fetch(apiURL('/locations'), requestOptions({ cache: 'no-store', headers: authenticatedHeaders() }));
        if (!response.ok) throw new Error(`Failed to fetch locations: HTTP ${response.status}`);
        const contentType = (response.headers.get('Content-Type') || '').toLowerCase();
        if (!contentType.startsWith('application/json')) {
            throw new Error(`Locations returned unexpected content type: ${contentType || 'missing'}`);
        }
        return readJSONBody(response, MAX_LOCATIONS_BODY_BYTES);
    }

    /**
     * Get Resource Timing entry for a URL (for precise timing)
     * Waits briefly for the entry to be recorded if not immediately available
     */
    async function getResourceTiming(url) {
        // Resource Timing API stores absolute URLs, so convert relative to absolute
        const absoluteUrl = new URL(url, browserPageURL()).href;

        // Try multiple times with increasing delays
        for (let attempt = 0; attempt < 5; attempt++) {
            const entries = performance.getEntriesByName(absoluteUrl, 'resource');
            if (entries.length > 0) {
                const entry = entries[entries.length - 1];
                if (entry.responseStart > 0 && entry.responseEnd > 0) {
                    return entry;
                }
            }
            // Wait increasingly longer for the browser to record the entry
            await new Promise(resolve => setTimeout(resolve, attempt * 5));
        }

        return null;
    }

    function createLoadActivity() {
        let active = 0;
        let gapGeneration = 0;
        let impreciseActive = 0;
        let impreciseGeneration = 0;

        return {
            begin(accurate = true) {
                const token = { accurate, ended: false };
                active++;
                if (!accurate) {
                    impreciseActive++;
                    impreciseGeneration++;
                }
                return token;
            },
            end(token) {
                if (!token || token.ended) return;
                token.ended = true;
                if (active > 0) {
                    active--;
                    if (active === 0) gapGeneration++;
                }
                if (!token.accurate && impreciseActive > 0) impreciseActive--;
            },
            snapshot() {
                return { active, gapGeneration, impreciseActive, impreciseGeneration };
            },
            async waitActive(timeoutMs = 2000, signal = abortController?.signal, shouldStop = () => false) {
                const deadline = performance.now() + timeoutMs;
                while (active <= 0) {
                    if (signal?.aborted) throw new DOMException('Aborted', 'AbortError');
                    if (shouldStop()) throw new Error('Sustained window ended');
                    if (performance.now() >= deadline) throw new Error('Timed out waiting for sustained load');
                    await sleep(2);
                }
            }
        };
    }

    function createStreamingUploadBody(bytes, activity) {
        const zeroChunk = new Uint8Array(64 * 1024);
        let remaining = bytes;
        let token = null;
        let ended = false;
        const finish = () => {
            if (ended) return;
            ended = true;
            if (activity) activity.end(token);
        };

        return {
            body: new ReadableStream({
                pull(controller) {
                    if (!token && activity) token = activity.begin(true);
                    if (remaining <= 0) {
                        controller.close();
                        finish();
                        return;
                    }
                    const count = Math.min(remaining, zeroChunk.byteLength);
                    controller.enqueue(count === zeroChunk.byteLength ? zeroChunk : zeroChunk.subarray(0, count));
                    remaining -= count;
                    // Close on the next pull, after fetch has consumed the final
                    // queued chunk. Closing and ending activity in this pull would
                    // mark the upload inactive while the last bytes were still
                    // buffered inside the request stream.
                },
                cancel() { finish(); }
            }),
            duplex: 'half',
            finish,
            loadTracking: 'request-stream'
        };
    }

    function responseFromXHR(xhr) {
        const headers = new Headers();
        const raw = xhr.getAllResponseHeaders() || '';
        for (const line of raw.trim().split(/[\r\n]+/)) {
            if (!line) continue;
            const index = line.indexOf(':');
            if (index > 0) headers.append(line.slice(0, index).trim(), line.slice(index + 1).trim());
        }
        return new Response(xhr.response || new ArrayBuffer(0), {
            status: xhr.status,
            statusText: xhr.statusText,
            headers
        });
    }

    function uploadWithXHR(url, payload, activity) {
        return new Promise((resolve, reject) => {
            const xhr = new XMLHttpRequest();
            let token = null;
            let finished = false;
            const finishActivity = () => {
                if (finished) return;
                finished = true;
                if (activity) activity.end(token);
            };

            xhr.open('POST', url, true);
            xhr.withCredentials = requestCredentialsMode() === 'include';
            xhr.responseType = 'arraybuffer';
            xhr.setRequestHeader('Content-Type', 'application/octet-stream');
            const tokenValue = accessToken();
            if (tokenValue) xhr.setRequestHeader('Authorization', `Bearer ${tokenValue}`);
            xhr.upload.onloadstart = () => {
                if (!token && activity) token = activity.begin(true);
            };
            xhr.upload.onloadend = finishActivity;
            xhr.onload = () => {
                finishActivity();
                resolve(responseFromXHR(xhr));
            };
            xhr.onerror = () => {
                finishActivity();
                reject(new Error('Upload network error'));
            };
            xhr.onabort = () => {
                finishActivity();
                reject(new DOMException('Aborted', 'AbortError'));
            };

            const signal = abortController?.signal;
            const abort = () => xhr.abort();
            if (signal) signal.addEventListener('abort', abort, { once: true });
            xhr.onloadend = () => {
                if (signal) signal.removeEventListener('abort', abort);
            };
            xhr.send(payload);
        });
    }

    /**
     * Run one exact, verified download request. When an activity tracker is
     * supplied, only response-body consumption is marked as active load.
     */
    async function runDownload(bytes, profile, runIndex, phase = null, activity = null) {
        if (!Number.isSafeInteger(bytes) || bytes < 0 || bytes > maxDownloadBytes()) {
            throw new Error(`Download size ${bytes} exceeds negotiated maximum ${maxDownloadBytes()}`);
        }

        const measId = `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
        let url = `/__down?bytes=${bytes}&measId=${measId}&profile=${encodeURIComponent(profile)}&run=${runIndex}`;
        if (phase) url += `&during=${encodeURIComponent(phase)}`;
        url = apiURL(url);

        const manualStart = performance.now();
        const response = await fetch(url, requestOptions({ cache: 'no-store', signal: abortController?.signal, headers: authenticatedHeaders() }));
        if (!response.ok) {
            const detail = (await response.text()).trim();
            throw new Error(`Download failed: HTTP ${response.status}${detail ? `: ${detail}` : ''}`);
        }
        const contentType = (response.headers.get('Content-Type') || '').toLowerCase();
        if (!contentType.startsWith('application/octet-stream')) {
            throw new Error(`Download returned unexpected content type: ${contentType || 'missing'}`);
        }
        const contentLength = response.headers.get('Content-Length');
        if (contentLength !== null && Number(contentLength) !== bytes) {
            throw new Error(`Download Content-Length ${contentLength}; expected ${bytes}`);
        }

        const responseStreaming = Boolean(response.body && typeof response.body.getReader === 'function');
        const token = activity?.begin(responseStreaming);
        let received;
        try {
            received = await consumeResponseBody(response, bytes);
        } finally {
            if (activity) activity.end(token);
        }
        const manualEnd = performance.now();
        if (received !== bytes) throw new Error(`Download received ${received} bytes; expected ${bytes}`);

        let durationMs;
        let timingSource;
        if (activity) {
            // A window uses one aggregate wall clock; looking up Resource Timing
            // for every component request would insert an artificial idle gap
            // before the worker can start its next bounded transfer.
            durationMs = manualEnd - manualStart;
            timingSource = 'window-component';
        } else {
            const timing = await getResourceTiming(url);
            if (timing && timing.responseStart > 0 && timing.responseEnd > 0) {
                const bodyTime = timing.responseEnd - timing.responseStart;
                if (bodyTime < 1 && timing.requestStart > 0) {
                    durationMs = timing.responseEnd - timing.requestStart;
                    timingSource = 'resource-timing-full';
                    resourceTimingUsed = true;
                } else if (bodyTime >= 1) {
                    durationMs = bodyTime;
                    timingSource = 'resource-timing';
                    resourceTimingUsed = true;
                }
            }
            if (!durationMs) {
                durationMs = manualEnd - manualStart;
                timingSource = 'manual';
                timingFallbackCount++;
                if (callbacks.onTimingWarning) callbacks.onTimingWarning('download', 'Resource Timing API unavailable');
            }
        }
        if (!Number.isFinite(durationMs) || durationMs <= 0) throw new Error(`Invalid download duration: ${durationMs}`);

        return {
            ts: Date.now(),
            direction: 'download',
            sizeBytes: received,
            durationMs,
            mbps: (received * 8) / (durationMs / 1000) / 1e6,
            profile,
            runIndex,
            timingSource
        };
    }

    /**
     * Run one exact, verified upload. Fixed-window callers pass a reusable
     * bounded payload or a request-stream factory; direct callers retain the
     * simple Uint8Array behavior used by the public measurement contract.
     */
    async function runUpload(bytes, profile, runIndex, phase = null, activity = null, bodySource = null) {
        if (uploadReceiptVersion < REQUIRED_UPLOAD_RECEIPT_VERSION) {
            throw new Error('Server does not support verified upload receipts');
        }
        const windowBody = typeof bodySource === 'function' || bodySource instanceof Uint8Array;
        const maximum = windowBody ? Math.min(serverMaxTransferBytes, MAX_WINDOW_CHUNK_BYTES) : maxUploadBytes();
        if (!Number.isSafeInteger(bytes) || bytes < 0 || bytes > maximum) {
            throw new Error(`Upload size ${bytes} exceeds browser maximum ${maximum}`);
        }

        const measId = `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
        let url = `/__up?measId=${measId}&profile=${encodeURIComponent(profile)}&run=${runIndex}`;
        if (phase) url += `&during=${encodeURIComponent(phase)}`;
        url = apiURL(url);

        let response;
        let descriptor = null;
        if (typeof bodySource === 'function') descriptor = bodySource(activity);
        const body = descriptor?.body || bodySource || new Uint8Array(bytes);

        if (!descriptor && activity && typeof XMLHttpRequest !== 'undefined' && body instanceof Uint8Array && xhrCanHonorCredentials(url)) {
            response = await uploadWithXHR(url, body, activity);
        } else {
            let token = null;
            if (activity && !descriptor) token = activity.begin(false);
            try {
                const options = {
                    method: 'POST',
                    body,
                    headers: authenticatedHeaders({ 'Content-Type': 'application/octet-stream' }),
                    cache: 'no-store',
                    signal: abortController?.signal
                };
                if (descriptor?.duplex) options.duplex = descriptor.duplex;
                response = await fetch(url, requestOptions(options));
            } finally {
                if (descriptor?.finish) descriptor.finish();
                if (activity && !descriptor) activity.end(token);
            }
        }

        if (!response.ok) {
            const detail = (await response.text()).trim();
            throw new Error(`Upload failed: HTTP ${response.status}${detail ? `: ${detail}` : ''}`);
        }
        const contentType = (response.headers.get('Content-Type') || '').toLowerCase();
        if (!contentType.startsWith('application/json')) {
            throw new Error(`Upload returned unexpected content type: ${contentType || 'missing'}`);
        }

        let receipt;
        try {
            receipt = await readJSONBody(response, MAX_UPLOAD_RECEIPT_BYTES);
        } catch (err) {
            throw new Error(`Invalid upload receipt: ${err.message}`);
        }
        if (!receipt || receipt.ok !== true) throw new Error('Server rejected upload');
        if (receipt.acceptedBytes !== bytes) {
            throw new Error(`Server accepted ${receipt.acceptedBytes} upload bytes; expected ${bytes}`);
        }
        const durationMs = Number(receipt.serverDurationNs) / 1e6;
        if (!Number.isFinite(durationMs) || durationMs <= 0) {
            throw new Error(`Invalid server upload duration: ${receipt.serverDurationNs}`);
        }

        return {
            ts: Date.now(),
            direction: 'upload',
            sizeBytes: receipt.acceptedBytes,
            durationMs,
            mbps: (receipt.acceptedBytes * 8) / (durationMs / 1000) / 1e6,
            profile,
            runIndex,
            timingSource: 'server-receipt'
        };
    }

    /**
     * Run a latency probe
     * Uses Resource Timing API when available for more accurate cross-browser measurements
     */
    async function runLatencyProbe(phase, seq) {
        const measId = `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        const url = apiURL(`/__down?bytes=0&measId=${measId}&during=${phase}&seq=${seq}`);

        const startedAt = Date.now();
        const manualStart = performance.now();
        const response = await fetch(url, requestOptions({
            cache: 'no-store',
            signal: abortController?.signal,
            headers: authenticatedHeaders()
        }));

        if (!response.ok) {
            const detail = (await response.text()).trim();
            throw new Error(`Latency probe failed: HTTP ${response.status}${detail ? `: ${detail}` : ''}`);
        }
        const received = await consumeResponseBody(response, 0);
        if (received !== 0) throw new Error(`Latency response contained ${received} bytes; expected 0`);
        const manualEnd = performance.now();

        // Try to get more accurate timing from Resource Timing API
        // requestStart to responseStart is closest to actual network RTT
        const timing = await getResourceTiming(url);
        let rttMs;
        let timingSource;

        if (timing && timing.requestStart > 0 && timing.responseStart > 0) {
            // Network time: from request sent to first byte received
            rttMs = timing.responseStart - timing.requestStart;
            timingSource = 'resource-timing';
            resourceTimingUsed = true;
        } else if (timing && timing.fetchStart > 0 && timing.responseEnd > 0) {
            // Fallback: total fetch time (includes more overhead but still from timing API)
            rttMs = timing.responseEnd - timing.fetchStart;
            timingSource = 'fetch-timing';
            resourceTimingUsed = true;
        } else {
            // Last resort: manual timing
            rttMs = manualEnd - manualStart;
            timingSource = 'manual';
            timingFallbackCount++;
            if (callbacks.onTimingWarning) {
                callbacks.onTimingWarning('latency', 'Resource Timing API unavailable');
            }
        }

        // Log first few probes to debug timing source
        if (seq < 3) {
            console.warn(`LATENCY PROBE ${seq}: ${rttMs.toFixed(2)}ms source=${timingSource}`,
                timing ? { requestStart: timing.requestStart, responseStart: timing.responseStart } : 'no timing');
        }

        const endedAt = Date.now();
        return {
            ts: endedAt,
            startedAt,
            endedAt,
            rttMs,
            phase,
            loadOverlapped: false,
            timingSource
        };
    }

    /**
     * Run warmup transfers to prime the connection
     */
    async function runWarmup() {
        // Browsers maintain ~6 connections per origin. We need to warm up
        // multiple connections in parallel to avoid alternating between
        // warm and cold connections during actual tests.
        try {
            // Run 6 parallel warmup downloads to prime multiple connections
            // Use ALL_*_PROFILES since dynamic profiles aren't set until estimation phase
            const warmupConcurrency = Math.max(1, Math.min(6, serverMaxConcurrentTransfersPerClient));
            const downloadPromises = [];
            for (let i = 0; i < warmupConcurrency; i++) {
                downloadPromises.push(
                    runDownload(ALL_DOWNLOAD_PROFILES['100kB'].bytes, 'warmup', i)
                        .catch(() => {}) // Ignore individual failures
                );
            }
            await Promise.all(downloadPromises);

            // Run 6 parallel warmup uploads
            const uploadPromises = [];
            for (let i = 0; i < warmupConcurrency; i++) {
                uploadPromises.push(
                    runUpload(ALL_UPLOAD_PROFILES['100kB'].bytes, 'warmup', i)
                        .catch(() => {})
                );
            }
            await Promise.all(uploadPromises);
        } catch (e) {
            // Warmup failures are non-fatal
            console.log('Warmup error (non-fatal):', e);
        }
    }

    /**
     * Quick bandwidth estimate for latency batching decision
     * Returns estimated Mbps or 0 on failure
     */
    async function quickBandwidthEstimate() {
        try {
            const bytes = 100 * 1000; // 100KB
            const url = apiURL(`/__down?bytes=${bytes}&measId=bw-check-${Date.now()}`);
            const start = performance.now();
            const response = await fetch(url, requestOptions({ cache: 'no-store', signal: abortController?.signal, headers: authenticatedHeaders() }));
            if (!response.ok) return 0;
            const received = await consumeResponseBody(response, bytes);
            if (received !== bytes) return 0;
            const durationMs = performance.now() - start;
            if (durationMs <= 0) return 0;
            const mbps = (received * 8) / (durationMs * 1000);
            return mbps;
        } catch {
            return 0;
        }
    }

    /**
     * Run unloaded latency tests with adaptive batching
     * Uses hybrid of latency and bandwidth to decide batching strategy
     */
    async function runUnloadedLatency() {
        const samples = [];
        const totalProbes = CONFIG.latencyProbes;
        const initialProbes = 3; // Run first 3 sequentially to estimate
        const batchSize = Math.max(1, Math.min(5, serverMaxConcurrentTransfersPerClient)); // respect server admission ceiling

        // Thresholds for batching decision
        const lowLatencyMs = 50;      // Below this: always parallel (fast/local)
        const highLatencyMs = 100;    // Above this: check bandwidth
        const minBandwidthMbps = 2;   // Minimum bandwidth for parallel on high-latency (probes are tiny)

        // Phase 1: Run initial probes sequentially to estimate connection quality
        for (let i = 0; i < Math.min(initialProbes, totalProbes); i++) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            try {
                const sample = await runLatencyProbe('unloaded', i);
                samples.push(sample);
                results.latencySamples.push(sample);

                if (callbacks.onLatencyProgress) {
                    callbacks.onLatencyProgress('unloaded', i + 1, totalProbes, sample);
                }
            } catch (err) {
                console.error(`Latency probe ${i} failed:`, err);
            }
        }

        if (samples.length === 0) {
            throw new Error('Unloaded latency test produced no valid samples');
        }
        if (samples.length >= totalProbes) {
            return samples;
        }

        // Calculate median RTT from initial probes
        const sortedRtts = samples.map(s => s.rttMs).sort((a, b) => a - b);
        const medianRtt = sortedRtts[Math.floor(sortedRtts.length / 2)];

        // Decide batching strategy based on latency and bandwidth
        let useParallel;
        if (medianRtt < lowLatencyMs) {
            // Low latency: definitely fast connection, use parallel
            useParallel = true;
            console.log(`Latency: median RTT ${medianRtt.toFixed(1)}ms (low), using parallel mode`);
        } else if (medianRtt >= highLatencyMs) {
            // High latency: check bandwidth to distinguish satellite from slow DSL
            const bandwidth = await quickBandwidthEstimate();
            useParallel = bandwidth >= minBandwidthMbps;
            console.log(`Latency: median RTT ${medianRtt.toFixed(1)}ms (high), bandwidth ~${bandwidth.toFixed(1)} Mbps, using ${useParallel ? 'parallel' : 'sequential'} mode`);
        } else {
            // Medium latency (50-100ms): typical internet, use parallel
            useParallel = true;
            console.log(`Latency: median RTT ${medianRtt.toFixed(1)}ms (medium), using parallel mode`);
        }

        // Phase 2: Run remaining probes (parallel or sequential based on connection quality)
        let probeIndex = initialProbes;

        if (useParallel) {
            // Fast connection: batch remaining probes
            while (probeIndex < totalProbes) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);

                const batchEnd = Math.min(probeIndex + batchSize, totalProbes);
                const batchPromises = [];
                for (let i = probeIndex; i < batchEnd; i++) {
                    batchPromises.push(
                        runLatencyProbe('unloaded', i).catch(err => {
                            console.error(`Latency probe ${i} failed:`, err);
                            return null;
                        })
                    );
                }

                const batchResults = await Promise.all(batchPromises);

                // Process each sample and call callback for each valid one
                let validCount = 0;
                for (let i = 0; i < batchResults.length; i++) {
                    const sample = batchResults[i];
                    if (sample) {
                        samples.push(sample);
                        results.latencySamples.push(sample);
                        validCount++;

                        if (callbacks.onLatencyProgress) {
                            callbacks.onLatencyProgress('unloaded', probeIndex + i + 1, totalProbes, sample);
                        }
                    }
                }

                // If all probes failed, still report progress
                if (validCount === 0 && callbacks.onLatencyProgress) {
                    callbacks.onLatencyProgress('unloaded', batchEnd, totalProbes, { rttMs: 0, phase: 'unloaded', ts: Date.now() });
                }

                probeIndex = batchEnd;
            }
        } else {
            // Slow/high-latency connection: continue sequential
            for (let i = probeIndex; i < totalProbes; i++) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);

                try {
                    const sample = await runLatencyProbe('unloaded', i);
                    samples.push(sample);
                    results.latencySamples.push(sample);

                    if (callbacks.onLatencyProgress) {
                        callbacks.onLatencyProgress('unloaded', i + 1, totalProbes, sample);
                    }
                } catch (err) {
                    console.error(`Latency probe ${i} failed:`, err);
                }
            }
        }

        if (samples.length < requiredSuccessfulRuns(totalProbes)) {
            throw new Error(`Unloaded latency test produced ${samples.length}/${totalProbes} valid samples`);
        }
        return samples;
    }

    function selectWindowPlan(estimatedMbps, maxBytes, direction = 'download', quick = false) {
        if (!Number.isFinite(estimatedMbps) || estimatedMbps <= 0) estimatedMbps = 10;

        let concurrency = 1;
        if (estimatedMbps >= 10000) concurrency = 16;
        else if (estimatedMbps >= 2000) concurrency = 8;
        else if (estimatedMbps >= 500) concurrency = 4;
        else if (estimatedMbps >= 100) concurrency = 2;
        // Reserve one transfer slot for the loaded-latency probe that runs
        // concurrently with the sustained load.
        const maxLoadConcurrency = Math.max(1, serverMaxConcurrentTransfersPerClient - 1);
        concurrency = Math.min(concurrency, MAX_BROWSER_CONCURRENCY, maxLoadConcurrency);

        const target = estimatedMbps * 1e6 / 8 * (TARGET_REQUEST_DURATION_MS / 1000) / concurrency;
        let chunkBytes = Math.ceil(target / 65536) * 65536;
        chunkBytes = Math.max(MIN_WINDOW_CHUNK_BYTES, chunkBytes);
        const clientMaximum = direction === 'upload'
            ? maxUploadWindowChunkBytes()
            : Math.min(maxDownloadBytes(), MAX_WINDOW_CHUNK_BYTES);
        chunkBytes = Math.min(chunkBytes, clientMaximum);
        if (Number.isSafeInteger(maxBytes) && maxBytes > 0) chunkBytes = Math.min(chunkBytes, maxBytes);

        return {
            chunkBytes,
            concurrency,
            windowDurationMs: quick ? 1000 : WINDOW_DURATION_MS,
            windows: quick ? 1 : WINDOW_COUNT,
            loadedWindow: quick ? 0 : LOADED_WINDOW_INDEX,
            loadedProbeCount: quick ? 3 : CONFIG.loadedLatencyProbes,
            direction
        };
    }

    async function runBaselineProfiles(direction) {
        const profiles = direction === 'download' ? ALL_DOWNLOAD_PROFILES : ALL_UPLOAD_PROFILES;
        const totalRuns = Object.values(profiles).reduce((sum, profile) => sum + profile.runs, 0);
        let completed = 0;
        const samples = [];

        for (const [profileName, profile] of Object.entries(profiles)) {
            let successful = 0;
            let lastError = null;
            for (let run = 0; run < profile.runs; run++) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);
                try {
                    const sample = direction === 'download'
                        ? await runDownload(profile.bytes, profileName, run)
                        : await runUpload(profile.bytes, profileName, run);
                    sample.sampleKind = 'baseline';
                    samples.push(sample);
                    results.throughputSamples.push(sample);
                    successful++;
                    completed++;
                    const callback = direction === 'download' ? callbacks.onDownloadProgress : callbacks.onUploadProgress;
                    if (callback) callback(profileName, run + 1, profile.runs, sample, completed, totalRuns);
                } catch (err) {
                    lastError = err;
                    console.error(`${direction} ${profileName} run ${run} failed:`, err);
                }
            }
            if (successful < requiredSuccessfulRuns(profile.runs)) {
                throw new Error(`${direction} baseline ${profileName} produced ${successful}/${profile.runs} valid samples${lastError ? `: ${lastError.message}` : ''}`);
            }
        }
        return samples;
    }

    async function runLoadedLatencyProbes(
        phase,
        count,
        activity,
        probeFunction = runLatencyProbe,
        shouldStop = () => false
    ) {
        const samples = [];
        const maxAttempts = count * 5;
        let lastError = null;

        for (let attempt = 0; attempt < maxAttempts && samples.length < count; attempt++) {
            if (shouldStop()) break;
            try {
                await activity.waitActive(2000, abortController?.signal, shouldStop);
                const before = activity.snapshot();
                if (before.active <= 0) continue;

                const sample = await probeFunction(phase, attempt);
                const after = activity.snapshot();
                const overlapped = before.active > 0 && after.active > 0 &&
                    before.gapGeneration === after.gapGeneration;
                if (!overlapped) {
                    lastError = new Error('probe did not remain inside a continuous load interval');
                    continue;
                }

                sample.loadOverlapped = true;
                sample.loadTrackingAccurate = before.impreciseActive === 0 && after.impreciseActive === 0 &&
                    before.impreciseGeneration === after.impreciseGeneration;
                samples.push(sample);
                if (callbacks.onLatencyProgress) {
                    callbacks.onLatencyProgress(phase, samples.length, count, sample);
                }
                await sleep(25);
            } catch (err) {
                if (err?.name === 'AbortError') throw err;
                lastError = err;
                if (shouldStop()) break;
            }
        }

        if (samples.length < requiredSuccessfulRuns(count)) {
            throw new Error(`${phase} loaded latency produced ${samples.length}/${count} continuously-overlapped probes${lastError ? `: ${lastError.message}` : ''}`);
        }
        return samples;
    }

    async function runThroughputWindow(direction, plan, windowIndex, withLoadedLatency) {
        const activity = createLoadActivity();
        let stopRequested = false;
        let bytesTransferred = 0;
        let requestCount = 0;
        let lastError = null;
        const profile = `window-${windowIndex + 1}`;

        let uploadBodySource = null;
        if (direction === 'upload') {
            uploadBodySource = supportsStreamingRequestBodies()
                ? activityForRequest => createStreamingUploadBody(plan.chunkBytes, activityForRequest)
                : new Uint8Array(plan.chunkBytes);
        }

        let releaseWorkers;
        const startGate = new Promise(resolve => { releaseWorkers = resolve; });
        const workers = Array.from({ length: plan.concurrency }, (_, workerIndex) => (async () => {
            await startGate;
            let requestIndex = 0;
            while (!stopRequested && !abortController?.signal.aborted) {
                const runIndex = workerIndex * 1000000 + requestIndex++;
                try {
                    const sample = direction === 'download'
                        ? await runDownload(plan.chunkBytes, profile, runIndex, direction, activity)
                        : await runUpload(plan.chunkBytes, profile, runIndex, direction, activity, uploadBodySource);
                    bytesTransferred += sample.sizeBytes;
                    requestCount++;
                } catch (err) {
                    if (err?.name === 'AbortError') return;
                    lastError = err;
                    await sleep(10);
                }
            }
        })());

        const windowStart = performance.now();
        releaseWorkers();
        let probes = [];
        let probeError = null;
        const probePromise = withLoadedLatency
            ? runLoadedLatencyProbes(
                direction,
                plan.loadedProbeCount,
                activity,
                runLatencyProbe,
                () => stopRequested
            )
                .then(value => { probes = value; })
                .catch(err => { probeError = err; })
            : Promise.resolve();

        // The timer stops new worker requests and probe retries. Already
        // in-flight transfers drain and remain in the aggregate, but a gap-prone
        // probe loop cannot extend a nominal 1.5-second window indefinitely.
        await sleep(plan.windowDurationMs);
        stopRequested = true;
        await Promise.all([Promise.all(workers), probePromise]);
        const windowEnd = performance.now();

        if (abortController?.signal.aborted) throw new DOMException('Aborted', 'AbortError');
        if (probeError) throw probeError;
        if (requestCount === 0 || bytesTransferred <= 0) {
            throw new Error(`${direction} window ${windowIndex + 1} completed no verified requests${lastError ? `: ${lastError.message}` : ''}`);
        }
        const durationMs = windowEnd - windowStart;
        if (!Number.isFinite(durationMs) || durationMs <= 0) {
            throw new Error(`${direction} window ${windowIndex + 1} has invalid duration ${durationMs}`);
        }

        return {
            sample: {
                ts: Date.now(),
                direction,
                sizeBytes: bytesTransferred,
                durationMs,
                mbps: (bytesTransferred * 8) / (durationMs / 1000) / 1e6,
                profile: 'window',
                runIndex: windowIndex,
                sampleKind: 'window',
                windowIndex,
                concurrency: plan.concurrency,
                chunkBytes: plan.chunkBytes,
                requestCount,
                timingSource: 'aggregate-wall-clock'
            },
            probes
        };
    }

    async function runSustainedWindows(direction, plan) {
        const windowSamples = [];
        const loadedSamples = [];
        for (let windowIndex = 0; windowIndex < plan.windows; windowIndex++) {
            const measurement = await runThroughputWindow(
                direction,
                plan,
                windowIndex,
                windowIndex === plan.loadedWindow
            );
            windowSamples.push(measurement.sample);
            loadedSamples.push(...measurement.probes);
            results.throughputSamples.push(measurement.sample);
            results.latencySamples.push(...measurement.probes);

            const callback = direction === 'download' ? callbacks.onDownloadProgress : callbacks.onUploadProgress;
            if (callback) callback(
                `window-${windowIndex + 1}`,
                1,
                1,
                measurement.sample,
                windowIndex + 1,
                plan.windows
            );
        }
        return { windowSamples, loadedSamples };
    }

    async function runDownloadTests() {
        const baseline = await runBaselineProfiles('download');
        const estimate = estimateSpeed(baseline.filter(sample => sample.profile === '1MB').map(sample => sample.mbps));
        const plan = selectWindowPlan(estimate, serverMaxTransferBytes, 'download');
        Object.assign(DOWNLOAD_PROFILES, ALL_DOWNLOAD_PROFILES, {
            window: { bytes: plan.chunkBytes, runs: plan.windows, concurrency: plan.concurrency }
        });
        console.log('Download fixed-window plan:', plan);
        return runSustainedWindows('download', plan);
    }

    async function runUploadTests() {
        const baseline = await runBaselineProfiles('upload');
        const estimate = estimateSpeed(baseline.filter(sample => sample.profile === '1MB').map(sample => sample.mbps));
        const plan = selectWindowPlan(estimate, serverMaxTransferBytes, 'upload');
        Object.assign(UPLOAD_PROFILES, ALL_UPLOAD_PROFILES, {
            window: { bytes: plan.chunkBytes, runs: plan.windows, concurrency: plan.concurrency }
        });
        console.log('Upload fixed-window plan:', plan);
        return runSustainedWindows('upload', plan);
    }

    function writeUint64(view, offset, value) {
        const normalized = Math.max(0, Math.floor(Number(value) || 0));
        const high = Math.floor(normalized / 0x100000000);
        const low = normalized % 0x100000000;
        view.setUint32(offset, high, false);
        view.setUint32(offset + 4, low, false);
    }

    function readUint64(view, offset) {
        return view.getUint32(offset, false) * 0x100000000 + view.getUint32(offset + 4, false);
    }

    function encodePacketFrame(sequence, sentAtUnixMilli, acknowledgement = false, recvAtUnixMilli = 0) {
        const frame = new Uint8Array(PACKET_FRAME_SIZE);
        frame.set(PACKET_FRAME_MAGIC, 0);
        frame[4] = PACKET_FRAME_VERSION;
        frame[5] = acknowledgement ? PACKET_FRAME_ACK : PACKET_FRAME_PROBE;
        const view = new DataView(frame.buffer);
        view.setUint16(6, PACKET_FRAME_HEADER_SIZE, false);
        view.setUint32(8, sequence >>> 0, false);
        writeUint64(view, 12, sentAtUnixMilli);
        writeUint64(view, 20, recvAtUnixMilli);
        view.setUint32(28, PACKET_FRAME_SIZE, false);
        for (let index = PACKET_FRAME_HEADER_SIZE; index < frame.byteLength; index++) {
            frame[index] = (sequence + index * 31) & 0xff;
        }
        return frame;
    }

    function decodePacketFrame(input) {
        let frame;
        if (input instanceof ArrayBuffer) frame = new Uint8Array(input);
        else if (ArrayBuffer.isView(input)) frame = new Uint8Array(input.buffer, input.byteOffset, input.byteLength);
        else throw new Error('Invalid packet-loss frame type');
        if (frame.byteLength !== PACKET_FRAME_SIZE) {
            throw new Error(`Invalid packet-loss frame size ${frame.byteLength}; want ${PACKET_FRAME_SIZE}`);
        }
        for (let index = 0; index < PACKET_FRAME_MAGIC.length; index++) {
            if (frame[index] !== PACKET_FRAME_MAGIC[index]) throw new Error('Invalid packet-loss frame magic');
        }
        if (frame[4] !== PACKET_FRAME_VERSION) {
            throw new Error(`Invalid packet-loss frame version ${frame[4]}`);
        }
        const view = new DataView(frame.buffer, frame.byteOffset, frame.byteLength);
        if (view.getUint16(6, false) !== PACKET_FRAME_HEADER_SIZE) throw new Error('Invalid packet-loss header size');
        if (view.getUint32(28, false) !== PACKET_FRAME_SIZE) throw new Error('Invalid declared packet-loss frame size');
        const sequence = view.getUint32(8, false);
        for (let index = PACKET_FRAME_HEADER_SIZE; index < frame.byteLength; index++) {
            if (frame[index] !== ((sequence + index * 31) & 0xff)) {
                throw new Error(`Corrupt packet-loss padding at byte ${index}`);
            }
        }
        if (frame[5] !== PACKET_FRAME_PROBE && frame[5] !== PACKET_FRAME_ACK) {
            throw new Error(`Invalid packet-loss frame type ${frame[5]}`);
        }
        return {
            acknowledgement: frame[5] === PACKET_FRAME_ACK,
            sequence,
            sentAtUnixMilli: readUint64(view, 12),
            recvAtUnixMilli: readUint64(view, 20)
        };
    }

    function boundedCount(value, maximum) {
        const integer = Number.isFinite(value) ? Math.floor(value) : 0;
        return Math.max(0, Math.min(maximum, integer));
    }

    function calculateLossPercent(sent, received) {
        if (sent <= 0) return 0;
        return ((sent - boundedCount(received, sent)) / sent) * 100;
    }

    function validatePacketReport(report, probesSent, acknowledgementsReceived) {
        const counters = [
            ['forwardReceived', report.forwardReceived],
            ['acknowledgementsSent', report.acknowledgementsSent],
            ['ackSendFailures', report.ackSendFailures],
            ['duplicateFrames', report.duplicateFrames],
            ['invalidFrames', report.invalidFrames]
        ];
        for (const [name, value] of counters) {
            if (!Number.isSafeInteger(value) || value < 0) {
                throw new Error(`Packet report ${name} is invalid: ${value}`);
            }
        }
        if (report.forwardReceived > probesSent) {
            throw new Error(`Packet report forwardReceived ${report.forwardReceived} exceeds probes sent ${probesSent}`);
        }
        if (report.acknowledgementsSent > report.forwardReceived) {
            throw new Error(`Packet report acknowledgementsSent ${report.acknowledgementsSent} exceeds forwardReceived ${report.forwardReceived}`);
        }
        if (report.ackSendFailures > report.forwardReceived ||
            report.acknowledgementsSent + report.ackSendFailures !== report.forwardReceived) {
            throw new Error('Packet report acknowledgement accounting is inconsistent');
        }
        if (!Number.isSafeInteger(acknowledgementsReceived) || acknowledgementsReceived < 0 ||
            acknowledgementsReceived > report.acknowledgementsSent) {
            throw new Error(`Client received ${acknowledgementsReceived} acknowledgements but daemon sent ${report.acknowledgementsSent}`);
        }
    }

    function unavailablePacketResult(reason, sent = 0, received = 0, testId = undefined) {
        return {
            sent,
            received,
            lossPercent: null,
            transactionLossPercent: null,
            forwardSent: sent,
            forwardReceived: 0,
            forwardLossPercent: null,
            acknowledgementsSent: 0,
            acknowledgementsReceived: received,
            reverseAcknowledgementLossPercent: null,
            frameSizeBytes: PACKET_FRAME_SIZE,
            duplicateFrames: 0,
            invalidFrames: 0,
            ackSendFailures: 0,
            rttStatsMs: { min: 0, median: 0, p90: 0 },
            jitterMs: 0,
            testId,
            unavailable: true,
            reason
        };
    }

    /**
     * Run an exact 1,200-byte WebRTC packet test and reconcile client ACKs with
     * the daemon's authoritative forward-path counters before accepting it.
     */
    async function runPacketLossTest() {
        let pc = null;
        let dc = null;
        let actualSent = 0;
        let acknowledgementCount = 0;
        let testId;

        try {
            if (measurementProtocolVersion < REQUIRED_MEASUREMENT_PROTOCOL_VERSION ||
                packetLossFrameVersion < REQUIRED_PACKET_LOSS_FRAME_VERSION) {
                const result = unavailablePacketResult(
                    `Server packet-loss protocol is too old (measurement ${measurementProtocolVersion}, frame ${packetLossFrameVersion})`
                );
                results.packetLoss = result;
                return result;
            }

            const credResponse = await fetch(apiURL('/api/turn/credentials'), requestOptions({
                cache: 'no-store',
                headers: authenticatedHeaders(),
                signal: abortController?.signal
            }));
            if (!credResponse.ok) throw new Error(`TURN credentials failed: HTTP ${credResponse.status}`);
            const credType = (credResponse.headers.get('Content-Type') || '').toLowerCase();
            if (!credType.startsWith('application/json')) throw new Error('TURN credentials returned non-JSON data');
            const turnCreds = await readJSONBody(credResponse, MAX_TURN_CREDENTIAL_BYTES);
            const servers = Array.isArray(turnCreds.servers) ? turnCreds.servers : [];
            const turnUrls = servers.filter(url => typeof url === 'string' && (url.startsWith('turn:') || url.startsWith('turns:')));
            if (turnUrls.length === 0) throw new Error('TURN server not configured');

            pc = new RTCPeerConnection({
                iceServers: [{
                    urls: turnUrls,
                    username: turnCreds.username,
                    credential: turnCreds.credential
                }],
                iceTransportPolicy: 'relay'
            });
            dc = pc.createDataChannel('packet-loss', { ordered: false, maxRetransmits: 0 });
            dc.binaryType = 'arraybuffer';

            const sendTimes = new Map();
            const acknowledgements = new Map();
            const rttSamples = [];
            dc.onmessage = event => {
                try {
                    const frame = decodePacketFrame(event.data);
                    if (!frame.acknowledgement || !sendTimes.has(frame.sequence) || acknowledgements.has(frame.sequence)) return;
                    acknowledgements.set(frame.sequence, frame);
                    const rtt = Date.now() - sendTimes.get(frame.sequence);
                    if (Number.isFinite(rtt) && rtt > 0 && rtt < 30000) rttSamples.push(rtt);
                } catch (err) {
                    console.warn('Ignoring invalid packet-loss acknowledgement:', err.message);
                }
            };

            let rejectOpen;
            const opened = new Promise((resolve, reject) => {
                rejectOpen = reject;
                const timeout = setTimeout(() => reject(new Error('ICE connection timeout')), 15000);
                dc.onopen = () => { clearTimeout(timeout); resolve(); };
                dc.onerror = () => { clearTimeout(timeout); reject(new Error('Data channel error')); };
            });
            pc.oniceconnectionstatechange = () => {
                if (pc.iceConnectionState === 'failed' || pc.iceConnectionState === 'disconnected') {
                    rejectOpen?.(new Error(`ICE connection ${pc.iceConnectionState}`));
                }
            };

            const offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            await new Promise((resolve, reject) => {
                const timeout = setTimeout(() => reject(new Error('ICE gathering timeout')), 10000);
                if (pc.iceGatheringState === 'complete') {
                    clearTimeout(timeout);
                    resolve();
                    return;
                }
                const complete = () => {
                    if (pc.iceGatheringState === 'complete') {
                        clearTimeout(timeout);
                        resolve();
                    }
                };
                pc.onicecandidate = event => { if (event.candidate === null) complete(); };
                pc.onicegatheringstatechange = complete;
            });

            const offerResponse = await fetch(apiURL('/api/packet-test/offer'), requestOptions({
                method: 'POST',
                headers: authenticatedHeaders({ 'Content-Type': 'application/json' }),
                cache: 'no-store',
                signal: abortController?.signal,
                body: JSON.stringify({
                    sdp: pc.localDescription.sdp,
                    type: pc.localDescription.type,
                    testProfile: 'loss-exact-v1'
                })
            }));
            if (!offerResponse.ok) throw new Error(`Packet test offer failed: HTTP ${offerResponse.status}`);
            const offerType = (offerResponse.headers.get('Content-Type') || '').toLowerCase();
            if (!offerType.startsWith('application/json')) throw new Error('Packet test offer returned non-JSON data');
            const answer = await readJSONBody(offerResponse, MAX_SIGNALING_BODY_BYTES);
            testId = answer.testId;
            if (!testId || !answer.sdp || answer.type !== 'answer') throw new Error('Packet test offer returned an invalid answer');
            await pc.setRemoteDescription({ sdp: answer.sdp, type: answer.type });
            if (dc.readyState !== 'open') await opened;

            for (let attempt = 0; attempt < CONFIG.packetLossPackets; attempt++) {
                if (abortController?.signal.aborted) throw new DOMException('Aborted', 'AbortError');
                const sentAt = Date.now();
                // Number only successfully submitted probes. A local send
                // failure is not network loss and must not create a sequence
                // hole in the loss-pattern analysis.
                const sequence = actualSent;
                try {
                    dc.send(encodePacketFrame(sequence, sentAt, false, 0));
                    sendTimes.set(sequence, sentAt);
                    actualSent++;
                } catch (_) {
                    // A send failure is not counted as an on-wire probe.
                }
                if (callbacks.onPacketLossProgress) {
                    callbacks.onPacketLossProgress(attempt + 1, CONFIG.packetLossPackets, acknowledgements.size);
                }
                await sleep(CONFIG.packetLossInterval);
            }
            if (actualSent === 0) throw new Error('No exact-size packet probes were sent');
            await sleep(CONFIG.packetLossExtraWait);

            acknowledgementCount = acknowledgements.size;
            const transactionLoss = calculateLossPercent(actualSent, acknowledgementCount);
            const cleanedRTT = cleanMeasurements(rttSamples);
            const rttStats = {
                min: percentile(cleanedRTT, 0),
                median: percentile(cleanedRTT, 50),
                p90: percentile(cleanedRTT, 90)
            };
            const jitterMs = jitter(cleanedRTT);

            results.dataChannelStats = await collectDataChannelStats(pc);
            results.lossPattern = analyzeLossPattern(actualSent, acknowledgements);

            const reportResponse = await fetch(apiURL('/api/packet-test/report'), requestOptions({
                method: 'POST',
                headers: authenticatedHeaders({ 'Content-Type': 'application/json' }),
                cache: 'no-store',
                signal: abortController?.signal,
                body: JSON.stringify({
                    testId,
                    sent: actualSent,
                    received: acknowledgementCount,
                    lossPercent: transactionLoss,
                    rttMinMs: rttStats.min,
                    rttMedianMs: rttStats.median,
                    rttP90Ms: rttStats.p90,
                    jitterMs
                })
            }));
            if (!reportResponse.ok) throw new Error(`Packet report failed: HTTP ${reportResponse.status}`);
            const reportType = (reportResponse.headers.get('Content-Type') || '').toLowerCase();
            if (!reportType.startsWith('application/json')) throw new Error('Packet report returned non-JSON data');
            const report = await readJSONBody(reportResponse, MAX_PACKET_REPORT_BYTES);
            if (!report.ok || report.protocolVersion < REQUIRED_MEASUREMENT_PROTOCOL_VERSION ||
                report.frameSizeBytes !== PACKET_FRAME_SIZE) {
                throw new Error('Packet report did not satisfy the exact-frame protocol');
            }
            validatePacketReport(report, actualSent, acknowledgementCount);

            const forwardReceived = report.forwardReceived;
            const acknowledgementsSent = report.acknowledgementsSent;
            const acknowledgementsReceived = acknowledgementCount;
            const forwardLoss = calculateLossPercent(actualSent, forwardReceived);
            const reverseLoss = acknowledgementsSent > 0
                ? calculateLossPercent(acknowledgementsSent, acknowledgementsReceived)
                : null;

            const result = {
                sent: actualSent,
                received: acknowledgementCount,
                lossPercent: transactionLoss,
                transactionLossPercent: transactionLoss,
                forwardSent: actualSent,
                forwardReceived,
                forwardLossPercent: forwardLoss,
                acknowledgementsSent,
                acknowledgementsReceived,
                reverseAcknowledgementLossPercent: reverseLoss,
                frameSizeBytes: report.frameSizeBytes,
                duplicateFrames: Math.max(0, Number(report.duplicateFrames) || 0),
                invalidFrames: Math.max(0, Number(report.invalidFrames) || 0),
                ackSendFailures: Math.max(0, Number(report.ackSendFailures) || 0),
                rttStatsMs: rttStats,
                jitterMs,
                testId
            };
            results.packetLoss = result;
            return result;
        } catch (err) {
            if (err?.name === 'AbortError') throw err;
            console.error('Packet loss test failed:', err);
            const result = unavailablePacketResult(
                err?.message || 'WebRTC packet test failed',
                actualSent,
                acknowledgementCount,
                testId
            );
            results.packetLoss = result;
            if (callbacks.onError) callbacks.onError('packetLoss', null, null, err);
            return result;
        } finally {
            try { dc?.close(); } catch (_) {}
            try { pc?.close(); } catch (_) {}
        }
    }

    function throughputValues(samples, direction) {
        const windows = [];
        const fallback = [];
        for (const sample of samples) {
            if (sample.direction !== direction || !Number.isFinite(sample.mbps) ||
                sample.mbps <= 0 || sample.durationMs < 10) continue;
            fallback.push(sample.mbps);
            if (sample.sampleKind === 'window' || sample.profile === 'window') windows.push(sample.mbps);
        }
        return filterOutliers(windows.length > 0 ? windows : fallback);
    }

    function latencyValues(samples, phase, requireOverlap = false) {
        const values = [];
        for (const sample of samples) {
            if (sample.phase !== phase || (requireOverlap && sample.loadOverlapped !== true)) continue;
            values.push(sample.rttMs);
        }
        return values;
    }

    /**
     * Calculate the same R-7/IQR summary as the Go client. Baselines tune the
     * window plan but never bias headline throughput when window samples exist.
     */
    function calculateSummary() {
        const download = throughputValues(results.throughputSamples, 'download');
        const upload = throughputValues(results.throughputSamples, 'upload');
        const unloaded = prepareLatency(latencyValues(results.latencySamples, 'unloaded'), 2);
        const downloadLoaded = filterOutliers(latencyValues(results.latencySamples, 'download', true));
        const uploadLoaded = filterOutliers(latencyValues(results.latencySamples, 'upload', true));
        const transactionLoss = results.packetLoss && !results.packetLoss.unavailable
            ? Number(results.packetLoss.transactionLossPercent ?? results.packetLoss.lossPercent)
            : null;

        return {
            downloadMbps: percentile(download, 90),
            uploadMbps: percentile(upload, 90),
            latencyUnloadedMs: percentile(unloaded, 50),
            latencyDownloadMs: percentile(downloadLoaded, 90),
            latencyUploadMs: percentile(uploadLoaded, 90),
            jitterMs: jitter(unloaded),
            packetLossPercent: Number.isFinite(transactionLoss) ? transactionLoss : null
        };
    }

    /**
     * Calculate network quality grades
     */
    function calculateQuality(summary) {
        console.log('Quality grading input:', {
            downloadMbps: summary.downloadMbps,
            uploadMbps: summary.uploadMbps,
            latencyUnloadedMs: summary.latencyUnloadedMs,
            jitterMs: summary.jitterMs,
            packetLossPercent: summary.packetLossPercent
        });
        const quality = {
            videoStreaming: gradeStreaming(summary),
            gaming: gradeGaming(summary),
            videoChatting: gradeVideoChatting(summary)
        };
        console.log('Quality grades:', quality);
        return quality;
    }

    function gradeStreaming(s) {
        if (!Number.isFinite(s.packetLossPercent)) return 'Incomplete';
        // Ensure we have valid numbers (NaN comparisons always return false)
        const dl = s.downloadMbps || 0;
        const lat = isNaN(s.latencyUnloadedMs) ? 999 : s.latencyUnloadedMs;
        const jit = isNaN(s.jitterMs) ? 999 : s.jitterMs;
        const loss = s.packetLossPercent;

        if (dl >= 50 && lat <= 25 && jit <= 5 && loss <= 0.5) return 'Great';
        if (dl >= 20 && lat <= 50 && jit <= 15 && loss <= 1.5) return 'Good';
        if (dl >= 10 && lat <= 80 && jit <= 30 && loss <= 3) return 'Okay';
        return 'Poor';
    }

    function gradeGaming(s) {
        if (!Number.isFinite(s.packetLossPercent)) return 'Incomplete';
        // Gaming requires low latency and jitter
        const dl = s.downloadMbps || 0;
        const lat = isNaN(s.latencyUnloadedMs) ? 999 : s.latencyUnloadedMs;
        const jit = isNaN(s.jitterMs) ? 999 : s.jitterMs;
        const loss = s.packetLossPercent;

        if (dl >= 25 && lat <= 20 && jit <= 5 && loss <= 0.1) return 'Great';
        if (dl >= 15 && lat <= 40 && jit <= 10 && loss <= 0.5) return 'Good';
        if (dl >= 5 && lat <= 80 && jit <= 20 && loss <= 1) return 'Okay';
        return 'Poor';
    }

    function gradeVideoChatting(s) {
        if (!Number.isFinite(s.packetLossPercent)) return 'Incomplete';
        // Video chat needs good upload and low latency
        const dl = s.downloadMbps || 0;
        const ul = s.uploadMbps || 0;
        const lat = isNaN(s.latencyUnloadedMs) ? 999 : s.latencyUnloadedMs;
        const jit = isNaN(s.jitterMs) ? 999 : s.jitterMs;
        const loss = s.packetLossPercent;

        if (dl >= 10 && ul >= 5 && lat <= 50 && jit <= 10 && loss <= 1) return 'Great';
        if (dl >= 5 && ul >= 2 && lat <= 100 && jit <= 20 && loss <= 2) return 'Good';
        if (dl >= 2 && ul >= 1 && lat <= 150 && jit <= 40 && loss <= 5) return 'Okay';
        return 'Poor';
    }

    function cleanMeasurements(values) {
        return values.filter(value => Number.isFinite(value) && value > 0);
    }

    function dropWarmup(values, count) {
        const clean = cleanMeasurements(values);
        if (count <= 0) return clean;
        if (count >= clean.length) return [];
        return clean.slice(count);
    }

    /** R-7 linear interpolation: rank = p/100 * (n-1). */
    function percentile(values, p) {
        const sorted = cleanMeasurements(values).sort((a, b) => a - b);
        if (sorted.length === 0) return 0;
        if (p <= 0) return sorted[0];
        if (p >= 100) return sorted[sorted.length - 1];
        const rank = (p / 100) * (sorted.length - 1);
        const lower = Math.floor(rank);
        const upper = Math.ceil(rank);
        if (lower === upper) return sorted[lower];
        const weight = rank - lower;
        return sorted[lower] + (sorted[upper] - sorted[lower]) * weight;
    }

    /** Conservative 1.5-IQR filter, retaining input if removal is destructive. */
    function filterOutliers(values) {
        const clean = cleanMeasurements(values);
        if (clean.length < 4) return clean;
        const q1 = percentile(clean, 25);
        const q3 = percentile(clean, 75);
        const iqr = q3 - q1;
        const lower = q1 - 1.5 * iqr;
        const upper = q3 + 1.5 * iqr;
        const filtered = clean.filter(value => value >= lower && value <= upper);
        return filtered.length * 2 < clean.length ? clean : filtered;
    }

    function prepareLatency(values, warmupCount) {
        return filterOutliers(dropWarmup(values, warmupCount));
    }

    function jitter(values) {
        const clean = cleanMeasurements(values);
        return clean.length === 0 ? 0 : percentile(clean, 90) - percentile(clean, 50);
    }

    function coefficientOfVariation(values) {
        const clean = cleanMeasurements(values);
        if (clean.length < 2) return 0;
        const mean = clean.reduce((sum, value) => sum + value, 0) / clean.length;
        if (mean <= 0) return 0;
        const squared = clean.reduce((sum, value) => sum + (value - mean) ** 2, 0);
        return Math.sqrt(squared / clean.length) / mean * 100;
    }

    /**
     * Helper: Sleep
     */
    function sleep(ms) {
        return new Promise(resolve => setTimeout(resolve, ms));
    }

    /**
     * Analyze loss pattern from packet loss test
     */
    function analyzeLossPattern(sent, acks) {
        // Single pass: collect losses and compute distribution/early count
        const bucketSize = sent / 10;
        const midpoint = sent / 2;
        const distribution = [0, 0, 0, 0, 0, 0, 0, 0, 0, 0];
        const losses = [];
        let earlyCount = 0;

        for (let i = 0; i < sent; i++) {
            if (!acks.has(i)) {
                losses.push(i);
                const bucket = Math.min(9, Math.floor(i / bucketSize));
                distribution[bucket]++;
                if (i < midpoint) earlyCount++;
            }
        }

        if (losses.length === 0) {
            return {
                type: 'none',
                burstCount: 0,
                maxBurstLength: 0,
                avgBurstLength: 0,
                lossDistribution: distribution,
                earlyLossPercent: 0,
                lateLossPercent: 0
            };
        }

        // Single pass for burst detection with inline max/sum
        let burstCount = 0;
        let maxBurstLength = 1;
        let totalBurstLength = 0;
        let currentBurst = 1;

        for (let i = 1; i < losses.length; i++) {
            if (losses[i] === losses[i - 1] + 1) {
                currentBurst++;
            } else {
                burstCount++;
                totalBurstLength += currentBurst;
                if (currentBurst > maxBurstLength) maxBurstLength = currentBurst;
                currentBurst = 1;
            }
        }
        // Don't forget the last burst
        burstCount++;
        totalBurstLength += currentBurst;
        if (currentBurst > maxBurstLength) maxBurstLength = currentBurst;

        const avgBurstLength = totalBurstLength / burstCount;
        const earlyLossPercent = (earlyCount / losses.length) * 100;
        const lateLossPercent = 100 - earlyLossPercent;

        // Classify pattern
        let type;
        if (maxBurstLength >= 10 || avgBurstLength > 3) {
            type = 'burst';
        } else if (lateLossPercent > 70) {
            type = 'tail';
        } else {
            type = 'random';
        }

        return {
            type,
            burstCount,
            maxBurstLength,
            avgBurstLength,
            lossDistribution: distribution,
            earlyLossPercent,
            lateLossPercent
        };
    }

    /**
     * Estimate bandwidth from samples
     */
    function estimateBandwidth(samples) {
        // Prefer sustained-window samples. Baselines exist only to size the
        // windows and can be dominated by setup timing on fast links.
        const dlWindows = [];
        const ulWindows = [];
        const dlFallback = [];
        const ulFallback = [];
        for (let i = 0; i < samples.length; i++) {
            const sample = samples[i];
            if (!Number.isFinite(sample.mbps) || sample.mbps <= 0) continue;
            const isWindow = sample.sampleKind === 'window' || sample.profile === 'window';
            if (sample.direction === 'download') {
                dlFallback.push(sample.mbps);
                if (isWindow) dlWindows.push(sample.mbps);
            } else if (sample.direction === 'upload') {
                ulFallback.push(sample.mbps);
                if (isWindow) ulWindows.push(sample.mbps);
            }
        }
        const dlSamples = dlWindows.length > 0 ? dlWindows : dlFallback;
        const ulSamples = ulWindows.length > 0 ? ulWindows : ulFallback;

        function stats(arr) {
            if (arr.length === 0) return { peak: 0, sustained: 0, variability: 0, trend: 'stable' };

            // Single pass for peak, sum, and partial sums for trend
            const n = arr.length;
            const third = Math.floor(n / 3);
            let peak = arr[0];
            let sum = 0;
            let firstThirdSum = 0;
            let lastThirdSum = 0;

            for (let i = 0; i < n; i++) {
                const v = arr[i];
                if (v > peak) peak = v;
                sum += v;
                if (i < third) firstThirdSum += v;
                if (i >= n - third) lastThirdSum += v;
            }

            const mean = sum / n;

            // Second pass for std (unavoidable - need mean first)
            let sumSqDiff = 0;
            for (let i = 0; i < n; i++) {
                const diff = arr[i] - mean;
                sumSqDiff += diff * diff;
            }
            const std = Math.sqrt(sumSqDiff / n);
            const variability = mean > 0 ? std / mean : 0;

            // Get p75 for sustained (requires sort)
            const sorted = [...arr].sort((a, b) => a - b);
            const sustained = sorted[Math.floor(n * 0.75)] || peak;

            // Trend calculation
            let trend = 'stable';
            if (third > 0) {
                const firstThirdAvg = firstThirdSum / third;
                const lastThirdAvg = lastThirdSum / third;
                const change = firstThirdAvg > 0 ? (lastThirdAvg - firstThirdAvg) / firstThirdAvg : 0;
                if (change > 0.1) trend = 'improving';
                else if (change < -0.1) trend = 'degrading';
            }

            return { peak, sustained, variability, trend };
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

    /**
     * Calculate network quality score (0-100)
     */
    function calculateNetworkQualityScore(summary, bandwidth) {
        // Defensive checks for inputs
        if (!summary || !bandwidth) {
            console.warn('calculateNetworkQualityScore: missing summary or bandwidth');
            return null;
        }

        if (!Number.isFinite(summary.packetLossPercent)) {
            console.warn('calculateNetworkQualityScore: packet loss unavailable');
            return null;
        }

        // Ensure we have valid numbers (default to safe values if NaN/undefined)
        const downloadMbps = summary.downloadMbps || 0;
        const latencyMs = isNaN(summary.latencyUnloadedMs) ? 50 : summary.latencyUnloadedMs;
        const jitterMs = isNaN(summary.jitterMs) ? 10 : summary.jitterMs;
        const packetLossPercent = summary.packetLossPercent;
        const downloadVariability = isNaN(bandwidth.downloadVariability) ? 0.1 : bandwidth.downloadVariability;

        // Bandwidth score (0-100)
        const bwScore = Math.min(100,
            (Math.log10(Math.max(1, downloadMbps)) / Math.log10(1000)) * 100
        );

        // Latency score (0-100)
        const latScore = Math.max(0, 100 - (latencyMs * 1.5));

        // Stability score (0-100)
        const jitterPenalty = Math.min(50, jitterMs * 3);
        const variabilityPenalty = Math.min(30, downloadVariability * 100);
        const stabScore = Math.max(0, 100 - jitterPenalty - variabilityPenalty);

        // Reliability score (0-100)
        const reliScore = Math.max(0, 100 - (packetLossPercent * 15));

        // Weighted composite
        const overall = Math.round(
            bwScore * 0.35 + latScore * 0.25 + stabScore * 0.20 + reliScore * 0.20
        );

        // Letter grade
        let grade;
        if (overall >= 95) grade = 'A+';
        else if (overall >= 85) grade = 'A';
        else if (overall >= 70) grade = 'B';
        else if (overall >= 55) grade = 'C';
        else if (overall >= 40) grade = 'D';
        else grade = 'F';

        const descriptions = {
            'A+': 'Exceptional - Suitable for any application',
            'A': 'Excellent - Great for gaming, streaming, and video calls',
            'B': 'Good - Suitable for most online activities',
            'C': 'Fair - May experience occasional issues with demanding applications',
            'D': 'Poor - Expect frequent buffering and lag',
            'F': 'Very Poor - Connection issues likely for most activities'
        };

        console.log('Network quality score calculated:', { overall, grade, bwScore, latScore, stabScore, reliScore });

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

    function countWindows(samples, direction) {
        return samples.filter(sample => sample.direction === direction &&
            (sample.sampleKind === 'window' || sample.profile === 'window')).length;
    }

    function countLatency(samples, phase, overlapOnly = false) {
        return samples.filter(sample => sample.phase === phase &&
            (!overlapOnly || sample.loadOverlapped === true)).length;
    }

    function hasImpreciseTiming(samples, latency) {
        for (const sample of samples) {
            if (sample.sampleKind === 'window' && sample.timingSource !== 'aggregate-wall-clock') return true;
        }
        for (const sample of latency) {
            if (sample.timingSource && sample.timingSource !== 'resource-timing') return true;
            if (sample.loadOverlapped && sample.loadTrackingAccurate === false) return true;
        }
        return false;
    }

    /** Five visible gates, matching the Go client and its deductions. */
    function assessTestConfidence(samples, latency, packetLoss, options = {}) {
        const downloadExpected = options.downloadExpected !== false;
        const uploadExpected = options.uploadExpected !== false;
        const warnings = [];

        const downloadValues = throughputValues(samples, 'download');
        const uploadValues = throughputValues(samples, 'upload');
        const unloadedValues = prepareLatency(latencyValues(latency, 'unloaded'), 2);
        const downloadWindows = countWindows(samples, 'download');
        const uploadWindows = countWindows(samples, 'upload');
        const unloadedCount = countLatency(latency, 'unloaded');
        const downloadLoaded = countLatency(latency, 'download', true);
        const uploadLoaded = countLatency(latency, 'upload', true);

        let sampleAdequate = unloadedCount >= 10;
        if (downloadExpected) sampleAdequate = sampleAdequate && downloadWindows >= 3 && downloadLoaded >= 3;
        if (uploadExpected) sampleAdequate = sampleAdequate && uploadWindows >= 3 && uploadLoaded >= 3;

        const downloadCV = coefficientOfVariation(downloadValues);
        const uploadCV = coefficientOfVariation(uploadValues);
        const latencyCV = coefficientOfVariation(unloadedValues);
        let variabilityAcceptable = latencyCV < 50;
        if (downloadExpected) variabilityAcceptable = variabilityAcceptable && downloadCV < 30;
        if (uploadExpected) variabilityAcceptable = variabilityAcceptable && uploadCV < 30;

        let overlapComplete = true;
        if (downloadExpected) overlapComplete = overlapComplete && downloadLoaded >= 3;
        if (uploadExpected) overlapComplete = overlapComplete && uploadLoaded >= 3;
        const packetComplete = packetLoss !== null && packetLoss !== undefined && !packetLoss.unavailable &&
            Number.isFinite(packetLoss.forwardLossPercent) &&
            Number.isFinite(packetLoss.reverseAcknowledgementLossPercent) &&
            Number(packetLoss.acknowledgementsSent) > 0;
        const timingAccurate = !hasImpreciseTiming(samples, latency);

        let score = 100;
        if (!sampleAdequate) {
            score -= 20;
            warnings.push('Insufficient fixed-window or latency samples for high confidence');
        }
        if (!variabilityAcceptable) {
            score -= 25;
            warnings.push('High variability in measurements');
        }
        if (!overlapComplete) {
            score -= 25;
            warnings.push('Loaded-latency overlap was incomplete');
        }
        if (!packetComplete) {
            score -= 20;
            warnings.push('Directional packet-loss test incomplete');
        }
        if (!timingAccurate) {
            score -= 10;
            warnings.push('Some measurements used fallback timing');
        }
        score = Math.max(0, score);

        return {
            overall: score >= 80 ? 'high' : score >= 50 ? 'medium' : 'low',
            overallScore: score,
            metrics: {
                sampleCount: {
                    downloadWindows,
                    uploadWindows,
                    unloadedLatency: unloadedCount,
                    downloadLoadedLatency: downloadLoaded,
                    uploadLoadedLatency: uploadLoaded,
                    adequate: sampleAdequate
                },
                coefficientOfVariation: {
                    download: downloadCV,
                    upload: uploadCV,
                    latency: latencyCV,
                    acceptable: variabilityAcceptable
                },
                loadedOverlap: {
                    downloadAccepted: downloadLoaded,
                    uploadAccepted: uploadLoaded,
                    complete: overlapComplete
                },
                timingAccuracy: { accurate: timingAccurate },
                packetTest: { completed: packetComplete }
            },
            warnings
        };
    }

    /**
     * Collect data channel stats from WebRTC peer connection
     */
    async function collectDataChannelStats(pc) {
        try {
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

            // Collect candidate IDs for lookups
            const candidateMap = new Map();

            stats.forEach(report => {
                // First pass: collect all candidates
                if (report.type === 'local-candidate' || report.type === 'remote-candidate') {
                    candidateMap.set(report.id, report);
                }
            });

            // Find active candidate-pair with multiple strategies
            let activePair = null;

            stats.forEach(report => {
                if (report.type === 'candidate-pair') {
                    // Strategy 1: nominated pair (Chrome)
                    // Strategy 2: succeeded state (Firefox/Safari)
                    // Strategy 3: in-progress with RTT (fallback)
                    const isActive = report.nominated ||
                                     report.state === 'succeeded' ||
                                     (report.state === 'in-progress' && report.currentRoundTripTime !== undefined);

                    if (isActive) {
                        // Prefer pairs with RTT data
                        if (!activePair || (report.currentRoundTripTime !== undefined && activePair.currentRoundTripTime === undefined)) {
                            activePair = report;
                        }
                    }
                }

                if (report.type === 'data-channel') {
                    bytesSent = report.bytesSent || 0;
                    bytesReceived = report.bytesReceived || 0;
                    messagesSent = report.messagesSent || 0;
                    messagesReceived = report.messagesReceived || 0;
                }
            });

            // Extract data from active pair
            if (activePair) {
                // Try Chrome-style currentRoundTripTime first
                if (activePair.currentRoundTripTime !== undefined) {
                    currentRoundTripTime = activePair.currentRoundTripTime * 1000;
                }
                // Firefox fallback: calculate from totalRoundTripTime / responsesReceived
                else if (activePair.totalRoundTripTime !== undefined && activePair.responsesReceived > 0) {
                    currentRoundTripTime = (activePair.totalRoundTripTime / activePair.responsesReceived) * 1000;
                }
                if (activePair.availableOutgoingBitrate !== undefined) {
                    availableOutgoingBitrate = activePair.availableOutgoingBitrate;
                }
                // Get candidate types from referenced candidates
                const localCandidate = candidateMap.get(activePair.localCandidateId);
                const remoteCandidate = candidateMap.get(activePair.remoteCandidateId);
                if (localCandidate) {
                    localCandidateType = localCandidate.candidateType;
                    if (localCandidate.protocol) protocol = localCandidate.protocol;
                }
                if (remoteCandidate) {
                    remoteCandidateType = remoteCandidate.candidateType;
                }
            }

            // Determine connection type
            if (localCandidateType === 'relay' || remoteCandidateType === 'relay') {
                connectionType = 'relay';
            } else if (localCandidateType === 'srflx' || remoteCandidateType === 'srflx') {
                connectionType = 'srflx';
            } else if (localCandidateType === 'prflx' || remoteCandidateType === 'prflx') {
                connectionType = 'prflx';
            } else if (localCandidateType === 'host' || remoteCandidateType === 'host') {
                connectionType = 'host';
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
                availableOutgoingBitrate,
                currentRoundTripTime
            };
        } catch (e) {
            console.error('Failed to collect data channel stats:', e);
            return null;
        }
    }

    /**
     * Start the full test suite
     */
    async function start() {
        if (isRunning) return;

        isRunning = true;
        isPaused = false;
        abortController = new AbortController();
        timingFallbackCount = 0;
        resourceTimingUsed = false;
        delete DOWNLOAD_PROFILES.window;
        delete UPLOAD_PROFILES.window;

        // Increase Resource Timing buffer to handle all our requests
        // Default is 150-250 entries which may not be enough
        if (typeof performance.setResourceTimingBufferSize === 'function') {
            performance.setResourceTimingBufferSize(500);
        }
        if (typeof performance.clearResourceTimings === 'function') {
            performance.clearResourceTimings();
        }

        // Reset results
        results = {
            meta: null,
            locations: [],
            throughputSamples: [],
            latencySamples: [],
            packetLoss: null,
            startTime: Date.now(),
            endTime: null,
            lossPattern: null,
            dataChannelStats: null,
            bandwidthEstimate: null,
            networkQualityScore: null,
            testConfidence: null
        };

        try {
            // Fetch metadata and locations
            if (callbacks.onProgress) callbacks.onProgress('meta', 0);

            const [meta, locations] = await Promise.all([
                fetchMeta(),
                fetchLocations()
            ]);

            results.meta = meta;
            results.locations = locations;

            if (Number.isSafeInteger(meta.maxTransferBytes) && meta.maxTransferBytes > 0) {
                serverMaxTransferBytes = meta.maxTransferBytes;
            } else {
                serverMaxTransferBytes = LEGACY_SERVER_TRANSFER_LIMIT_BYTES;
            }
            serverMaxConcurrentTransfersPerClient = Number.isSafeInteger(meta.maxConcurrentTransfersPerClient) && meta.maxConcurrentTransfersPerClient > 0
                ? meta.maxConcurrentTransfersPerClient
                : 24;
            if (serverMaxConcurrentTransfersPerClient < 2) {
                throw new Error(`Server per-client transfer limit ${serverMaxConcurrentTransfersPerClient} is too low for loaded-latency measurement; need at least 2`);
            }
            measurementProtocolVersion = Number(meta.measurementProtocolVersion) || 0;
            uploadReceiptVersion = Number(meta.uploadReceiptVersion) || 0;
            packetLossFrameVersion = Number(meta.packetLossFrameVersion) || 0;
            if (serverMaxTransferBytes < ALL_DOWNLOAD_PROFILES['1MB'].bytes) {
                throw new Error(`Server transfer limit ${serverMaxTransferBytes} is below the 1MB baseline profile`);
            }
            if (measurementProtocolVersion < REQUIRED_MEASUREMENT_PROTOCOL_VERSION) {
                throw new Error(`Server measurement protocol ${measurementProtocolVersion} is too old; need version ${REQUIRED_MEASUREMENT_PROTOCOL_VERSION}`);
            }
            if (uploadReceiptVersion < REQUIRED_UPLOAD_RECEIPT_VERSION) {
                throw new Error(`Server does not support verified upload receipts (need version ${REQUIRED_UPLOAD_RECEIPT_VERSION})`);
            }

            if (callbacks.onMetaReceived) {
                callbacks.onMetaReceived(meta, locations);
            }

            // Run unloaded latency baseline
            if (callbacks.onProgress) callbacks.onProgress('latency', 0);
            await runUnloadedLatency();

            // Warmup: small transfers to establish connection and get past TCP slow start
            if (callbacks.onProgress) callbacks.onProgress('warmup', 0);
            await runWarmup();

            // Run download tests
            if (callbacks.onProgress) callbacks.onProgress('download', 0);
            await runDownloadTests();

            // Run upload tests
            if (callbacks.onProgress) callbacks.onProgress('upload', 0);
            await runUploadTests();

            // Loaded latency probes run inside the middle sustained download
            // and upload windows. There is deliberately no post-load probe phase.
            if (callbacks.onProgress) callbacks.onProgress('loaded-latency', 100);

            // Run packet loss test
            if (callbacks.onProgress) callbacks.onProgress('packet-loss', 0);
            await runPacketLossTest();

            results.endTime = Date.now();

            // Calculate final summary
            const summary = calculateSummary();
            const quality = calculateQuality(summary);

            // Calculate enhanced metrics
            results.bandwidthEstimate = estimateBandwidth(results.throughputSamples);
            results.networkQualityScore = calculateNetworkQualityScore(summary, results.bandwidthEstimate);
            results.testConfidence = assessTestConfidence(
                results.throughputSamples,
                results.latencySamples,
                results.packetLoss
            );

            if (callbacks.onComplete) {
                callbacks.onComplete(results, summary, quality);
            }

            return { results, summary, quality };
        } catch (err) {
            if (err.name !== 'AbortError') {
                console.error('Speed test failed:', err);
                if (callbacks.onError) {
                    callbacks.onError('general', null, null, err);
                }
            }
            throw err;
        } finally {
            isRunning = false;
            abortController = null;
        }
    }

    /**
     * Stop the test
     */
    function stop() {
        if (abortController) {
            abortController.abort();
        }
        isRunning = false;
        isPaused = false;
    }

    /**
     * Pause the test
     */
    function pause() {
        isPaused = true;
    }

    /**
     * Resume the test
     */
    function resume() {
        isPaused = false;
    }

    /**
     * Get current results
     */
    function getResults() {
        return results;
    }

    /**
     * Check if test is running
     */
    function getIsRunning() {
        return isRunning;
    }

    /**
     * Check if test is paused
     */
    function getIsPaused() {
        return isPaused;
    }

    /**
     * Export results as JSON
     */
    function exportResults() {
        const summary = calculateSummary();
        const quality = calculateQuality(summary);

        return JSON.stringify({
            meta: results.meta,
            summary,
            quality,
            throughputSamples: results.throughputSamples,
            latencySamples: results.latencySamples,
            packetLoss: results.packetLoss,
            bandwidthEstimate: results.bandwidthEstimate,
            networkQualityScore: results.networkQualityScore,
            testConfidence: results.testConfidence,
            startTime: results.startTime,
            endTime: results.endTime
        }, null, 2);
    }

    // Public API
    const api = {
        setCallbacks,
        start,
        stop,
        pause,
        resume,
        getResults,
        getIsRunning,
        getIsPaused,
        exportResults,
        calculateSummary,
        calculateQuality,
        fetchMeta,
        fetchLocations,
        DOWNLOAD_PROFILES,
        UPLOAD_PROFILES,
        CONFIG
    };

    // Keep low-level measurement hooks out of the browser API while making the
    // wire contract directly testable under Node.
    if (typeof module !== 'undefined' && module.exports) {
        api.__test = {
            fetchMeta,
            fetchLocations,
            apiURL,
            requestCredentialsMode,
            xhrCanHonorCredentials,
            runDownload,
            runUpload,
            selectWindowPlan,
            createLoadActivity,
            createStreamingUploadBody,
            runLoadedLatencyProbes,
            runThroughputWindow,
            encodePacketFrame,
            decodePacketFrame,
            validatePacketReport,
            percentile,
            prepareLatency,
            calculateJitter: jitter,
            coefficientOfVariation,
            assessTestConfidence,
            calculateSummary,
            setResults(value) { results = value; },
            resetRequestStreamingSupport() { requestStreamingSupport = undefined; },
            setServerCapabilities(maxBytes, receiptVersion, protocolVersion = 2, frameVersion = 1, maxClientTransfers = 24) {
                serverMaxTransferBytes = maxBytes;
                serverMaxConcurrentTransfersPerClient = maxClientTransfers;
                uploadReceiptVersion = receiptVersion;
                measurementProtocolVersion = protocolVersion;
                packetLossFrameVersion = frameVersion;
            }
        };
    }

    return api;
})();

// Export for module systems
if (typeof module !== 'undefined' && module.exports) {
    module.exports = SpeedTest;
}
