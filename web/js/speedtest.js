/**
 * Speedtest measurement module
 * Handles download, upload, latency, and packet loss tests
 */

const SpeedTest = (function() {
    'use strict';

    // Profile configurations
    const DOWNLOAD_PROFILES = {
        '100k': 100 * 1024,
        '1M': 1 * 1024 * 1024,
        '10M': 10 * 1024 * 1024,
        '25M': 25 * 1024 * 1024,
        '100M': 100 * 1024 * 1024
    };

    const UPLOAD_PROFILES = {
        '100k': 100 * 1024,
        '1M': 1 * 1024 * 1024,
        '10M': 10 * 1024 * 1024,
        '25M': 25 * 1024 * 1024,
        '50M': 50 * 1024 * 1024
    };

    // Test configuration
    const CONFIG = {
        runsPerProfile: 4,
        latencyProbes: 20,
        loadedLatencyProbes: 5,
        packetLossPackets: 1000,
        packetLossInterval: 10,
        packetLossExtraWait: 3000
    };

    // State
    let abortController = null;
    let isRunning = false;
    let isPaused = false;

    // Results storage
    let results = {
        meta: null,
        locations: [],
        throughputSamples: [],
        latencySamples: [],
        packetLoss: null,
        startTime: null,
        endTime: null
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
        onError: null
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
        const response = await fetch('/meta', { cache: 'no-store' });
        if (!response.ok) throw new Error('Failed to fetch metadata');
        return response.json();
    }

    /**
     * Fetch locations from server
     */
    async function fetchLocations() {
        const response = await fetch('/locations', { cache: 'no-store' });
        if (!response.ok) throw new Error('Failed to fetch locations');
        return response.json();
    }

    /**
     * Get Resource Timing entry for a URL (for precise timing)
     */
    function getResourceTiming(url) {
        const entries = performance.getEntriesByName(url, 'resource');
        if (entries.length > 0) {
            return entries[entries.length - 1]; // Get most recent
        }
        return null;
    }

    /**
     * Run a single download test
     */
    async function runDownload(bytes, profile, runIndex, phase = null) {
        const measId = `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        let url = `/__down?bytes=${bytes}&measId=${measId}&profile=${profile}&run=${runIndex}`;
        if (phase) url += `&during=${phase}`;

        // Clear any existing entries for this URL pattern
        performance.clearResourceTimings();

        const response = await fetch(url, {
            cache: 'no-store',
            signal: abortController?.signal
        });

        if (!response.ok) throw new Error(`Download failed: ${response.status}`);

        const reader = response.body.getReader();
        let received = 0;

        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            received += value.byteLength;
        }

        // Use Resource Timing API to get precise body transfer time
        // responseStart = first byte received, responseEnd = last byte received
        const timing = getResourceTiming(url);
        let durationMs;

        if (timing && timing.responseStart > 0 && timing.responseEnd > 0) {
            // Precise: just the body transfer time (excludes connection, TLS, headers)
            durationMs = timing.responseEnd - timing.responseStart;
        } else {
            // Fallback to our manual timing
            durationMs = performance.now() - (timing?.startTime || 0);
        }

        const mbps = (received * 8) / (durationMs / 1000) / 1e6;

        return {
            ts: Date.now(),
            direction: 'download',
            sizeBytes: received,
            durationMs,
            mbps,
            profile,
            runIndex
        };
    }

    /**
     * Run a single upload test
     */
    async function runUpload(bytes, profile, runIndex, phase = null) {
        const measId = `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        let url = `/__up?measId=${measId}&profile=${profile}&run=${runIndex}`;
        if (phase) url += `&during=${phase}`;

        const payload = new Uint8Array(bytes);

        // Clear any existing entries
        performance.clearResourceTimings();

        const response = await fetch(url, {
            method: 'POST',
            body: payload,
            headers: { 'Content-Type': 'application/octet-stream' },
            signal: abortController?.signal
        });

        if (!response.ok) throw new Error(`Upload failed: ${response.status}`);
        await response.arrayBuffer();

        // Use Resource Timing API for precise timing
        // For uploads: requestStart to responseStart = time to send body + server processing
        const timing = getResourceTiming(url);
        let durationMs;

        if (timing && timing.requestStart > 0 && timing.responseStart > 0) {
            // Precise: request send time (excludes connection setup, includes minimal server processing)
            durationMs = timing.responseStart - timing.requestStart;
        } else {
            // Fallback
            durationMs = performance.now() - (timing?.startTime || 0);
        }

        const mbps = (bytes * 8) / (durationMs / 1000) / 1e6;

        return {
            ts: Date.now(),
            direction: 'upload',
            sizeBytes: bytes,
            durationMs,
            mbps,
            profile,
            runIndex
        };
    }

    /**
     * Run a latency probe
     */
    async function runLatencyProbe(phase, seq) {
        const measId = `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        const url = `/__down?bytes=0&measId=${measId}&during=${phase}&seq=${seq}`;

        const start = performance.now();
        const response = await fetch(url, {
            cache: 'no-store',
            signal: abortController?.signal
        });

        if (!response.ok) throw new Error(`Latency probe failed: ${response.status}`);
        await response.arrayBuffer();

        const end = performance.now();
        const rttMs = end - start;

        return {
            ts: Date.now(),
            rttMs,
            phase
        };
    }

    /**
     * Run warmup transfers to prime the connection
     */
    async function runWarmup() {
        // Do a few small downloads and uploads to warm up the connection
        // This gets past TCP slow start and establishes keep-alive
        try {
            // Small download warmups
            for (let i = 0; i < 3; i++) {
                await runDownload(DOWNLOAD_PROFILES['1M'], 'warmup', i);
            }
            // Small upload warmups
            for (let i = 0; i < 3; i++) {
                await runUpload(UPLOAD_PROFILES['1M'], 'warmup', i);
            }
        } catch (e) {
            // Warmup failures are non-fatal
            console.log('Warmup error (non-fatal):', e);
        }
    }

    /**
     * Run unloaded latency tests
     */
    async function runUnloadedLatency() {
        const samples = [];
        for (let i = 0; i < CONFIG.latencyProbes; i++) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            const sample = await runLatencyProbe('unloaded', i);
            samples.push(sample);
            results.latencySamples.push(sample);

            if (callbacks.onLatencyProgress) {
                callbacks.onLatencyProgress('unloaded', i + 1, CONFIG.latencyProbes, sample);
            }
        }
        return samples;
    }

    /**
     * Run download tests for all profiles
     */
    async function runDownloadTests() {
        const profiles = Object.entries(DOWNLOAD_PROFILES);
        let totalRuns = 0;
        const totalExpected = profiles.length * CONFIG.runsPerProfile;

        for (const [profile, bytes] of profiles) {
            for (let run = 0; run < CONFIG.runsPerProfile; run++) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);

                try {
                    const sample = await runDownload(bytes, profile, run);
                    results.throughputSamples.push(sample);
                    totalRuns++;

                    if (callbacks.onDownloadProgress) {
                        callbacks.onDownloadProgress(profile, run + 1, CONFIG.runsPerProfile, sample, totalRuns, totalExpected);
                    }
                } catch (err) {
                    console.error(`Download ${profile} run ${run} failed:`, err);
                    if (callbacks.onError) {
                        callbacks.onError('download', profile, run, err);
                    }
                }
            }
        }
    }

    /**
     * Run upload tests for all profiles
     */
    async function runUploadTests() {
        const profiles = Object.entries(UPLOAD_PROFILES);
        let totalRuns = 0;
        const totalExpected = profiles.length * CONFIG.runsPerProfile;

        for (const [profile, bytes] of profiles) {
            for (let run = 0; run < CONFIG.runsPerProfile; run++) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);

                try {
                    const sample = await runUpload(bytes, profile, run);
                    results.throughputSamples.push(sample);
                    totalRuns++;

                    if (callbacks.onUploadProgress) {
                        callbacks.onUploadProgress(profile, run + 1, CONFIG.runsPerProfile, sample, totalRuns, totalExpected);
                    }
                } catch (err) {
                    console.error(`Upload ${profile} run ${run} failed:`, err);
                    if (callbacks.onError) {
                        callbacks.onError('upload', profile, run, err);
                    }
                }
            }
        }
    }

    /**
     * Run latency probes during download
     */
    async function runLatencyDuringDownload() {
        // Start a medium download in background
        const downloadPromise = runDownload(DOWNLOAD_PROFILES['10M'], '10M', 0, 'download');

        // Run latency probes concurrently
        const probePromises = [];
        for (let i = 0; i < CONFIG.loadedLatencyProbes; i++) {
            await sleep(200); // Stagger probes
            if (abortController?.signal.aborted) break;
            probePromises.push(runLatencyProbe('download', i).then(sample => {
                results.latencySamples.push(sample);
                if (callbacks.onLatencyProgress) {
                    callbacks.onLatencyProgress('download', i + 1, CONFIG.loadedLatencyProbes, sample);
                }
                return sample;
            }));
        }

        await Promise.all([downloadPromise, ...probePromises]);
    }

    /**
     * Run latency probes during upload
     */
    async function runLatencyDuringUpload() {
        // Start a medium upload in background
        const uploadPromise = runUpload(UPLOAD_PROFILES['10M'], '10M', 0, 'upload');

        // Run latency probes concurrently
        const probePromises = [];
        for (let i = 0; i < CONFIG.loadedLatencyProbes; i++) {
            await sleep(200); // Stagger probes
            if (abortController?.signal.aborted) break;
            probePromises.push(runLatencyProbe('upload', i).then(sample => {
                results.latencySamples.push(sample);
                if (callbacks.onLatencyProgress) {
                    callbacks.onLatencyProgress('upload', i + 1, CONFIG.loadedLatencyProbes, sample);
                }
                return sample;
            }));
        }

        await Promise.all([uploadPromise, ...probePromises]);
    }

    /**
     * Run packet loss test via WebRTC
     */
    async function runPacketLossTest() {
        try {
            // Fetch TURN credentials
            const credResponse = await fetch('/api/turn/credentials', { credentials: 'include' });
            if (!credResponse.ok) {
                // TURN not configured - return unavailable result
                const unavailableResult = {
                    sent: 0,
                    received: 0,
                    lossPercent: 0,
                    rttStatsMs: { min: 0, median: 0, p90: 0 },
                    jitterMs: 0,
                    unavailable: true,
                    reason: 'TURN server not configured'
                };
                results.packetLoss = unavailableResult;
                return unavailableResult;
            }
            const turnCreds = await credResponse.json();

            // Create RTCPeerConnection
            // Separate STUN (no credentials) from TURN/TURNS (with credentials)
            const iceServers = [];
            const stunUrls = turnCreds.servers.filter(s => s.startsWith('stun:') || s.startsWith('stuns:'));
            const turnUrls = turnCreds.servers.filter(s => s.startsWith('turn:') || s.startsWith('turns:'));

            if (stunUrls.length > 0) {
                iceServers.push({ urls: stunUrls });
            }
            if (turnUrls.length > 0) {
                iceServers.push({
                    urls: turnUrls,
                    username: turnCreds.username,
                    credential: turnCreds.credential
                });
            }

            const pc = new RTCPeerConnection({
                iceServers: iceServers,
                iceTransportPolicy: 'all'
            });

            // Create data channel for packet loss test
            const dc = pc.createDataChannel('packet-loss', {
                ordered: false,
                maxRetransmits: 0
            });

            // Create offer and set local description to start ICE gathering
            const offer = await pc.createOffer();
            await pc.setLocalDescription(offer);

            // Now wait for ICE gathering to complete
            await new Promise((resolve, reject) => {
                const timeout = setTimeout(() => reject(new Error('ICE gathering timeout')), 10000);

                // Check if already complete
                if (pc.iceGatheringState === 'complete') {
                    clearTimeout(timeout);
                    resolve();
                    return;
                }

                pc.onicecandidate = (event) => {
                    if (event.candidate === null) {
                        clearTimeout(timeout);
                        resolve();
                    }
                };

                pc.onicegatheringstatechange = () => {
                    if (pc.iceGatheringState === 'complete') {
                        clearTimeout(timeout);
                        resolve();
                    }
                };
            });

            // Exchange SDP with server
            const offerResponse = await fetch('/api/packet-test/offer', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                credentials: 'include',
                body: JSON.stringify({
                    sdp: pc.localDescription.sdp,
                    type: pc.localDescription.type,
                    testProfile: 'loss-basic'
                })
            });

            if (!offerResponse.ok) {
                throw new Error('Packet test offer failed');
            }

            const answer = await offerResponse.json();
            await pc.setRemoteDescription(new RTCSessionDescription({
                sdp: answer.sdp,
                type: answer.type
            }));

            const testId = answer.testId;

            // Wait for data channel to open
            await new Promise((resolve, reject) => {
                const timeout = setTimeout(() => reject(new Error('Data channel timeout')), 15000);

                if (dc.readyState === 'open') {
                    clearTimeout(timeout);
                    resolve();
                } else {
                    dc.onopen = () => {
                        clearTimeout(timeout);
                        resolve();
                    };
                    dc.onerror = (err) => {
                        clearTimeout(timeout);
                        reject(err);
                    };
                }
            });

            // Run packet loss test
            const N = CONFIG.packetLossPackets;
            const acks = new Map();
            const rttSamples = [];
            let seq = 0;

            dc.onmessage = async (event) => {
                try {
                    // Handle both string and Blob data (pion sends binary by default)
                    let data = event.data;
                    if (data instanceof Blob) {
                        data = await data.text();
                    }
                    const msg = JSON.parse(data);
                    if (typeof msg.ack === 'number' && typeof msg.receivedAt === 'number') {
                        acks.set(msg.ack, msg.receivedAt);
                        // Calculate RTT if we have the send time
                        const sendTime = msg.sentAt;
                        if (sendTime) {
                            rttSamples.push(Date.now() - sendTime);
                        }
                    }
                } catch (e) {
                    console.log('Failed to parse ack:', event.data, e);
                }
            };

            // Send packets
            await new Promise((resolve) => {
                const interval = setInterval(() => {
                    if (seq >= N || abortController?.signal.aborted) {
                        clearInterval(interval);
                        resolve();
                        return;
                    }

                    const msg = {
                        seq,
                        sentAt: Date.now(),
                        size: 1200
                    };
                    try {
                        dc.send(JSON.stringify(msg));
                    } catch (e) {
                        // Channel may have closed
                    }
                    seq++;

                    if (callbacks.onPacketLossProgress) {
                        callbacks.onPacketLossProgress(seq, N, acks.size);
                    }
                }, CONFIG.packetLossInterval);
            });

            // Wait for late acks
            await sleep(CONFIG.packetLossExtraWait);

            // Calculate results
            const sent = N;
            const received = acks.size;
            const lossPercent = ((sent - received) / sent) * 100;

            // Calculate RTT stats
            rttSamples.sort((a, b) => a - b);
            const rttMin = rttSamples.length > 0 ? rttSamples[0] : 0;
            const rttMedian = rttSamples.length > 0 ? rttSamples[Math.floor(rttSamples.length / 2)] : 0;
            const rttP90 = rttSamples.length > 0 ? rttSamples[Math.floor(rttSamples.length * 0.9)] : 0;

            // Calculate jitter (average deviation from mean)
            let jitterMs = 0;
            if (rttSamples.length > 1) {
                const mean = rttSamples.reduce((a, b) => a + b, 0) / rttSamples.length;
                jitterMs = rttSamples.reduce((sum, rtt) => sum + Math.abs(rtt - mean), 0) / rttSamples.length;
            }

            const result = {
                sent,
                received,
                lossPercent,
                rttStatsMs: {
                    min: rttMin,
                    median: rttMedian,
                    p90: rttP90
                },
                jitterMs,
                testId
            };

            results.packetLoss = result;

            // Clean up
            dc.close();
            pc.close();

            // Report results (optional)
            try {
                await fetch('/api/packet-test/report', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        testId,
                        sent,
                        received,
                        lossPercent,
                        rttMinMs: rttMin,
                        rttMedianMs: rttMedian,
                        rttP90Ms: rttP90,
                        jitterMs
                    })
                });
            } catch (e) {
                // Ignore report failures
            }

            return result;
        } catch (err) {
            console.error('Packet loss test failed:', err);
            if (callbacks.onError) {
                callbacks.onError('packetLoss', null, null, err);
            }
            return null;
        }
    }

    /**
     * Calculate summary statistics
     */
    function calculateSummary() {
        const dlSamples = results.throughputSamples
            .filter(s => s.direction === 'download')
            .map(s => s.mbps);
        const ulSamples = results.throughputSamples
            .filter(s => s.direction === 'upload')
            .map(s => s.mbps);

        const latUnloaded = results.latencySamples
            .filter(s => s.phase === 'unloaded')
            .map(s => s.rttMs);
        const latDownload = results.latencySamples
            .filter(s => s.phase === 'download')
            .map(s => s.rttMs);
        const latUpload = results.latencySamples
            .filter(s => s.phase === 'upload')
            .map(s => s.rttMs);

        return {
            downloadMbps: percentile(dlSamples, 90),
            uploadMbps: percentile(ulSamples, 90),
            latencyUnloadedMs: percentile(latUnloaded, 50),
            latencyDownloadMs: percentile(latDownload, 90),
            latencyUploadMs: percentile(latUpload, 90),
            jitterMs: latUnloaded.length > 0
                ? percentile(latUnloaded, 90) - percentile(latUnloaded, 50)
                : 0,
            packetLossPercent: results.packetLoss ? results.packetLoss.lossPercent : 0
        };
    }

    /**
     * Calculate network quality grades
     */
    function calculateQuality(summary) {
        console.log('Grading with summary:', JSON.stringify(summary, null, 2));
        const quality = {
            videoStreaming: gradeStreaming(summary),
            gaming: gradeGaming(summary),
            videoChatting: gradeVideoChatting(summary)
        };
        console.log('Calculated quality:', quality);
        return quality;
    }

    function gradeStreaming(s) {
        // Ensure we have valid numbers (NaN comparisons always return false)
        const dl = s.downloadMbps || 0;
        const lat = isNaN(s.latencyUnloadedMs) ? 999 : s.latencyUnloadedMs;
        const jit = isNaN(s.jitterMs) ? 999 : s.jitterMs;
        const loss = isNaN(s.packetLossPercent) ? 100 : s.packetLossPercent;

        if (dl >= 50 && lat <= 25 && jit <= 5 && loss <= 0.5) return 'Great';
        if (dl >= 20 && lat <= 50 && jit <= 15 && loss <= 1.5) return 'Good';
        if (dl >= 10 && lat <= 80 && jit <= 30 && loss <= 3) return 'Okay';
        return 'Poor';
    }

    function gradeGaming(s) {
        // Gaming requires low latency and jitter
        const dl = s.downloadMbps || 0;
        const lat = isNaN(s.latencyUnloadedMs) ? 999 : s.latencyUnloadedMs;
        const jit = isNaN(s.jitterMs) ? 999 : s.jitterMs;
        const loss = isNaN(s.packetLossPercent) ? 100 : s.packetLossPercent;

        if (dl >= 25 && lat <= 20 && jit <= 3 && loss <= 0.1) return 'Great';
        if (dl >= 15 && lat <= 40 && jit <= 10 && loss <= 0.5) return 'Good';
        if (dl >= 5 && lat <= 80 && jit <= 20 && loss <= 2) return 'Okay';
        return 'Poor';
    }

    function gradeVideoChatting(s) {
        // Video chat needs good upload and low latency
        const dl = s.downloadMbps || 0;
        const ul = s.uploadMbps || 0;
        const minSpeed = Math.min(dl, ul);
        const lat = isNaN(s.latencyUnloadedMs) ? 999 : s.latencyUnloadedMs;
        const jit = isNaN(s.jitterMs) ? 999 : s.jitterMs;
        const loss = isNaN(s.packetLossPercent) ? 100 : s.packetLossPercent;

        if (minSpeed >= 10 && lat <= 30 && jit <= 5 && loss <= 0.5) return 'Great';
        if (minSpeed >= 5 && lat <= 50 && jit <= 15 && loss <= 1) return 'Good';
        if (minSpeed >= 2 && lat <= 100 && jit <= 30 && loss <= 3) return 'Okay';
        return 'Poor';
    }

    /**
     * Helper: Calculate percentile
     */
    function percentile(arr, p) {
        if (arr.length === 0) return 0;
        const sorted = [...arr].sort((a, b) => a - b);
        const idx = Math.ceil((p / 100) * sorted.length) - 1;
        return sorted[Math.max(0, idx)];
    }

    /**
     * Helper: Sleep
     */
    function sleep(ms) {
        return new Promise(resolve => setTimeout(resolve, ms));
    }

    /**
     * Start the full test suite
     */
    async function start() {
        if (isRunning) return;

        isRunning = true;
        isPaused = false;
        abortController = new AbortController();

        // Reset results
        results = {
            meta: null,
            locations: [],
            throughputSamples: [],
            latencySamples: [],
            packetLoss: null,
            startTime: Date.now(),
            endTime: null
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

            // Run loaded latency tests
            if (callbacks.onProgress) callbacks.onProgress('loaded-latency', 0);
            await runLatencyDuringDownload();
            await runLatencyDuringUpload();

            // Run packet loss test
            if (callbacks.onProgress) callbacks.onProgress('packet-loss', 0);
            await runPacketLossTest();

            results.endTime = Date.now();

            // Calculate final summary
            const summary = calculateSummary();
            const quality = calculateQuality(summary);

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
            startTime: results.startTime,
            endTime: results.endTime
        }, null, 2);
    }

    // Public API
    return {
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
})();

// Export for module systems
if (typeof module !== 'undefined' && module.exports) {
    module.exports = SpeedTest;
}
