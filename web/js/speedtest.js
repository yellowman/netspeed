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
     * Waits briefly for the entry to be recorded if not immediately available
     */
    async function getResourceTiming(url) {
        // Resource Timing API stores absolute URLs, so convert relative to absolute
        const absoluteUrl = new URL(url, window.location.origin).href;

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

    /**
     * Run a single download test
     */
    async function runDownload(bytes, profile, runIndex, phase = null) {
        const measId = `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        let url = `/__down?bytes=${bytes}&measId=${measId}&profile=${profile}&run=${runIndex}`;
        if (phase) url += `&during=${phase}`;

        // Capture start time for manual fallback timing
        const manualStart = performance.now();

        const response = await fetch(url, {
            cache: 'no-store',
            signal: abortController?.signal
        });

        if (!response.ok) throw new Error(`Download failed: ${response.status}`);

        // Use arrayBuffer() to ensure response is fully received before checking timing
        const buffer = await response.arrayBuffer();
        const received = buffer.byteLength;

        const manualEnd = performance.now();

        // Use Resource Timing API to get precise body transfer time
        // responseStart = first byte received, responseEnd = last byte received
        const timing = await getResourceTiming(url);
        let durationMs;
        let timingSource;

        if (timing && timing.responseStart > 0 && timing.responseEnd > 0) {
            // Body transfer time (excludes connection, TLS, request headers)
            const bodyTime = timing.responseEnd - timing.responseStart;

            // For small/fast downloads, body time may be 0 or near-0.
            // Fall back to requestStart->responseEnd which includes request overhead but avoids Infinity
            if (bodyTime < 1) {
                durationMs = timing.responseEnd - timing.requestStart;
                timingSource = 'resource-timing-full';
            } else {
                durationMs = bodyTime;
                timingSource = 'resource-timing';
            }
        } else {
            // Fallback: use manual timing (includes connection overhead)
            durationMs = manualEnd - manualStart;
            timingSource = 'manual';
            console.log('Download timing fallback:', { profile, runIndex, timing, manualMs: durationMs });
        }

        // Final guard against division by zero
        if (durationMs < 0.1) {
            durationMs = manualEnd - manualStart;
            timingSource = 'manual-guard';
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

        // Capture start time for manual fallback timing
        const manualStart = performance.now();

        const response = await fetch(url, {
            method: 'POST',
            body: payload,
            headers: { 'Content-Type': 'application/octet-stream' },
            signal: abortController?.signal
        });

        if (!response.ok) throw new Error(`Upload failed: ${response.status}`);
        await response.arrayBuffer();

        const manualEnd = performance.now();

        // Use Resource Timing API for precise timing
        const timing = await getResourceTiming(url);
        let durationMs;

        // For uploads, prefer Server-Timing header if available (most accurate)
        // Server-Timing measures server-side: time from request start to body fully received
        if (timing && timing.serverTiming && timing.serverTiming.length > 0) {
            const serverDur = timing.serverTiming.find(st => st.name === 'app');
            if (serverDur && serverDur.duration > 0) {
                durationMs = serverDur.duration;
            }
        }

        // Fallback to Resource Timing requestStart -> responseStart
        if (!durationMs && timing && timing.requestStart > 0 && timing.responseStart > 0) {
            durationMs = timing.responseStart - timing.requestStart;
        }

        // Last fallback: manual timing
        if (!durationMs) {
            durationMs = manualEnd - manualStart;
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
     * Uses Resource Timing API when available for more accurate cross-browser measurements
     */
    async function runLatencyProbe(phase, seq) {
        const measId = `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
        const url = `/__down?bytes=0&measId=${measId}&during=${phase}&seq=${seq}`;

        const manualStart = performance.now();
        const response = await fetch(url, {
            cache: 'no-store',
            signal: abortController?.signal
        });

        if (!response.ok) throw new Error(`Latency probe failed: ${response.status}`);
        await response.arrayBuffer();
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
        } else if (timing && timing.fetchStart > 0 && timing.responseEnd > 0) {
            // Fallback: total fetch time (includes more overhead but still from timing API)
            rttMs = timing.responseEnd - timing.fetchStart;
            timingSource = 'fetch-timing';
        } else {
            // Last resort: manual timing
            rttMs = manualEnd - manualStart;
            timingSource = 'manual';
        }

        // Log first few probes to debug timing source
        if (seq < 3) {
            console.warn(`LATENCY PROBE ${seq}: ${rttMs.toFixed(2)}ms source=${timingSource}`,
                timing ? { requestStart: timing.requestStart, responseStart: timing.responseStart } : 'no timing');
        }

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
        // Browsers maintain ~6 connections per origin. We need to warm up
        // multiple connections in parallel to avoid alternating between
        // warm and cold connections during actual tests.
        try {
            // Run 6 parallel warmup downloads to prime multiple connections
            const downloadPromises = [];
            for (let i = 0; i < 6; i++) {
                downloadPromises.push(
                    runDownload(DOWNLOAD_PROFILES['100k'], 'warmup', i)
                        .catch(() => {}) // Ignore individual failures
                );
            }
            await Promise.all(downloadPromises);

            // Run 6 parallel warmup uploads
            const uploadPromises = [];
            for (let i = 0; i < 6; i++) {
                uploadPromises.push(
                    runUpload(UPLOAD_PROFILES['100k'], 'warmup', i)
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
            // Use arraybuffer for synchronous decoding (avoids race condition with async Blob.text())
            dc.binaryType = 'arraybuffer';

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

            const textDecoder = new TextDecoder();
            dc.onmessage = (event) => {
                try {
                    // Synchronously decode ArrayBuffer or handle string
                    let data = event.data;
                    if (data instanceof ArrayBuffer) {
                        data = textDecoder.decode(data);
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

            console.log('Packet loss test results:', {
                sent,
                received,
                lossPercent: lossPercent.toFixed(2) + '%',
                rttSamplesCount: rttSamples.length,
                rttSamplesSlice: rttSamples.slice(0, 5)
            });

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
        const dlSamplesRaw = results.throughputSamples
            .filter(s => s.direction === 'download')
            .map(s => s.mbps);
        const ulSamplesRaw = results.throughputSamples
            .filter(s => s.direction === 'upload')
            .map(s => s.mbps);

        // Filter outliers from throughput samples - timing precision issues can
        // cause inflated speeds for small/fast transfers
        const dlSamples = filterOutliers(dlSamplesRaw);
        const ulSamples = filterOutliers(ulSamplesRaw);

        // Get all unloaded latency samples, then skip the first 2 which often have
        // cold-start overhead (connection setup, TLS, etc) that skews jitter
        const allLatUnloaded = results.latencySamples
            .filter(s => s.phase === 'unloaded')
            .map(s => s.rttMs);
        const latUnloadedRaw = allLatUnloaded.slice(2); // Skip first 2 probes

        // Filter outliers using IQR method to remove browser timing artifacts
        const latUnloaded = filterOutliers(latUnloadedRaw);

        const latDownload = results.latencySamples
            .filter(s => s.phase === 'download')
            .map(s => s.rttMs);
        const latUpload = results.latencySamples
            .filter(s => s.phase === 'upload')
            .map(s => s.rttMs);

        console.log('Sample counts:', {
            downloads: dlSamples.length,
            uploads: ulSamples.length,
            latencyUnloaded: latUnloaded.length,
            packetLoss: results.packetLoss
        });
        console.log('Sample values:', {
            dlSamples: dlSamples.slice(0, 5),
            ulSamples: ulSamples.slice(0, 5),
            latUnloaded: latUnloaded.slice(0, 5)
        });

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
     * Helper: Filter outliers using IQR method
     * Removes values that are more than 1.5*IQR below Q1 or above Q3
     * This helps remove browser timing artifacts from latency measurements
     */
    function filterOutliers(arr) {
        if (arr.length < 4) return arr; // Need enough samples for IQR

        const sorted = [...arr].sort((a, b) => a - b);
        const q1 = sorted[Math.floor(sorted.length * 0.25)];
        const q3 = sorted[Math.floor(sorted.length * 0.75)];
        const iqr = q3 - q1;

        const lower = q1 - 1.5 * iqr;
        const upper = q3 + 1.5 * iqr;

        const filtered = arr.filter(v => v >= lower && v <= upper);

        // If we filtered too much, return original to avoid empty/skewed results
        if (filtered.length < arr.length * 0.5) {
            console.log('Outlier filter removed too many samples, using original', {
                original: arr.length,
                filtered: filtered.length,
                q1, q3, iqr, lower, upper
            });
            return arr;
        }

        if (filtered.length < arr.length) {
            console.log('Filtered outliers from latency samples:', {
                original: arr.length,
                filtered: filtered.length,
                removed: arr.filter(v => v < lower || v > upper)
            });
        }

        return filtered;
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

        // Increase Resource Timing buffer to handle all our requests
        // Default is 150-250 entries which may not be enough
        if (typeof performance.setResourceTimingBufferSize === 'function') {
            performance.setResourceTimingBufferSize(500);
        }
        performance.clearResourceTimings();

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
