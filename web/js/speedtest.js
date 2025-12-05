/**
 * Speedtest measurement module
 * Handles download, upload, latency, and packet loss tests
 */

const SpeedTest = (function() {
    'use strict';

    // All available profile configurations
    // Profiles scale from slow connections (128 Kbps) to extremely fast (1 Tbps)
    const ALL_DOWNLOAD_PROFILES = {
        '100kB': { bytes: 100 * 1000, runs: 10 },
        '1MB':   { bytes: 1 * 1000 * 1000, runs: 8 },
        '10MB':  { bytes: 10 * 1000 * 1000, runs: 6 },
        '25MB':  { bytes: 25 * 1000 * 1000, runs: 4 },
        '100MB': { bytes: 100 * 1000 * 1000, runs: 3 },
        '250MB': { bytes: 250 * 1000 * 1000, runs: 2 },
        '500MB': { bytes: 500 * 1000 * 1000, runs: 2 },    // 1s at 4 Gbps
        '1GB':   { bytes: 1000 * 1000 * 1000, runs: 2 },   // 1s at 8 Gbps
        '2GB':   { bytes: 2000 * 1000 * 1000, runs: 2 },   // 1s at 16 Gbps
        '5GB':   { bytes: 5000 * 1000 * 1000, runs: 2 },   // 1s at 40 Gbps
        '12GB':  { bytes: 12000 * 1000 * 1000, runs: 2 },  // 1s at ~100 Gbps
        '50GB':  { bytes: 50000 * 1000 * 1000, runs: 2 },  // 1s at 400 Gbps
        '100GB': { bytes: 100000 * 1000 * 1000, runs: 2 }, // 1s at 800 Gbps
        '125GB': { bytes: 125000 * 1000 * 1000, runs: 2 }  // 1s at 1 Tbps
    };

    const ALL_UPLOAD_PROFILES = {
        '100kB': { bytes: 100 * 1000, runs: 8 },
        '1MB':   { bytes: 1 * 1000 * 1000, runs: 6 },
        '10MB':  { bytes: 10 * 1000 * 1000, runs: 4 },
        '25MB':  { bytes: 25 * 1000 * 1000, runs: 4 },
        '50MB':  { bytes: 50 * 1000 * 1000, runs: 3 },
        '100MB': { bytes: 100 * 1000 * 1000, runs: 2 },
        '250MB': { bytes: 250 * 1000 * 1000, runs: 2 },    // 1s at 2 Gbps
        '500MB': { bytes: 500 * 1000 * 1000, runs: 2 },    // 1s at 4 Gbps
        '1GB':   { bytes: 1000 * 1000 * 1000, runs: 2 },   // 1s at 8 Gbps
        '2GB':   { bytes: 2000 * 1000 * 1000, runs: 2 },   // 1s at 16 Gbps
        '5GB':   { bytes: 5000 * 1000 * 1000, runs: 2 },   // 1s at 40 Gbps
        '12GB':  { bytes: 12000 * 1000 * 1000, runs: 2 },  // 1s at ~100 Gbps
        '50GB':  { bytes: 50000 * 1000 * 1000, runs: 2 },  // 1s at 400 Gbps
        '100GB': { bytes: 100000 * 1000 * 1000, runs: 2 }, // 1s at 800 Gbps
        '125GB': { bytes: 125000 * 1000 * 1000, runs: 2 }  // 1s at 1 Tbps
    };

    // Maximum duration (seconds) for a single test to be included
    // Profiles are selected if their estimated transfer time is under this limit
    // This scales linearly from 128 Kbps to 1 Tbps
    const MAX_TEST_DURATION_SECONDS = 4;

    // Total time budget for download/upload test phases (seconds)
    // Tests stop when this budget is exhausted
    const TOTAL_DOWNLOAD_DURATION_SECONDS = 8;
    const TOTAL_UPLOAD_DURATION_SECONDS = 8;

    // Active profiles (set dynamically based on detected speed)
    let DOWNLOAD_PROFILES = {};
    let UPLOAD_PROFILES = {};

    // Test configuration
    const CONFIG = {
        latencyProbes: 20,
        loadedLatencyProbes: 5,
        packetLossPackets: 1000,
        packetLossInterval: 10,
        packetLossExtraWait: 3000
    };

    /**
     * Calculate estimated transfer time in seconds
     */
    function estimateTransferTime(bytes, speedMbps) {
        if (speedMbps <= 0) return Infinity;
        // time = bits / bits_per_second
        // bits = bytes * 8, bits_per_second = speedMbps * 1,000,000
        return (bytes * 8) / (speedMbps * 1e6);
    }

    /**
     * Select appropriate download profiles based on estimated speed
     * Uses linear scaling: include profiles where transfer time < MAX_TEST_DURATION_SECONDS
     */
    function selectDownloadProfiles(estimatedSpeedMbps) {
        // Always include baseline profiles
        const profiles = {
            '100kB': ALL_DOWNLOAD_PROFILES['100kB'],
            '1MB': ALL_DOWNLOAD_PROFILES['1MB']
        };

        // Check larger profiles based on estimated transfer time
        const largerProfiles = ['10MB', '25MB', '100MB', '250MB', '500MB', '1GB', '2GB', '5GB', '12GB', '50GB', '100GB', '125GB'];
        for (const name of largerProfiles) {
            const profile = ALL_DOWNLOAD_PROFILES[name];
            if (!profile) continue;

            const estimatedSeconds = estimateTransferTime(profile.bytes, estimatedSpeedMbps);
            if (estimatedSeconds <= MAX_TEST_DURATION_SECONDS) {
                profiles[name] = profile;
            }
        }

        console.log(`Download profiles for ${estimatedSpeedMbps.toFixed(1)} Mbps:`, Object.keys(profiles));
        return profiles;
    }

    /**
     * Select appropriate upload profiles based on estimated speed
     * Uses linear scaling: include profiles where transfer time < MAX_TEST_DURATION_SECONDS
     */
    function selectUploadProfiles(estimatedSpeedMbps) {
        // Always include baseline profiles
        const profiles = {
            '100kB': ALL_UPLOAD_PROFILES['100kB'],
            '1MB': ALL_UPLOAD_PROFILES['1MB']
        };

        // Check larger profiles based on estimated transfer time
        const largerProfiles = ['10MB', '25MB', '50MB', '100MB', '250MB', '500MB', '1GB', '2GB', '5GB', '12GB', '50GB', '100GB', '125GB'];
        for (const name of largerProfiles) {
            const profile = ALL_UPLOAD_PROFILES[name];
            if (!profile) continue;

            const estimatedSeconds = estimateTransferTime(profile.bytes, estimatedSpeedMbps);
            if (estimatedSeconds <= MAX_TEST_DURATION_SECONDS) {
                profiles[name] = profile;
            }
        }

        // Skip 50MB if 100MB will be included (100MB provides better data)
        if (profiles['100MB'] && profiles['50MB']) {
            delete profiles['50MB'];
        }

        console.log(`Upload profiles for ${estimatedSpeedMbps.toFixed(1)} Mbps:`, Object.keys(profiles));
        return profiles;
    }

    /**
     * Estimate speed from test samples using median (robust to outliers)
     * Being conservative avoids selecting profiles that take too long
     */
    function estimateSpeed(samples) {
        if (samples.length === 0) return 0;
        const sorted = [...samples].sort((a, b) => a - b);
        // Use median - robust to outlier spikes that could cause
        // profile selection based on unsustainable burst speeds
        const midIndex = Math.floor(sorted.length / 2);
        if (sorted.length % 2 === 0) {
            // Even number of samples: average the two middle values
            return (sorted[midIndex - 1] + sorted[midIndex]) / 2;
        }
        return sorted[midIndex];
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
            if (bodyTime < 1 && timing.requestStart > 0) {
                durationMs = timing.responseEnd - timing.requestStart;
                timingSource = 'resource-timing-full';
                resourceTimingUsed = true;
            } else if (bodyTime >= 1) {
                durationMs = bodyTime;
                timingSource = 'resource-timing';
                resourceTimingUsed = true;
            }
            // If bodyTime < 1 and requestStart is 0, fall through to manual timing
        }

        if (!durationMs) {
            // Fallback: use manual timing (includes connection overhead)
            durationMs = manualEnd - manualStart;
            timingSource = 'manual';
            timingFallbackCount++;
            console.log('Download timing fallback:', { profile, runIndex, timing, manualMs: durationMs });
            if (callbacks.onTimingWarning) {
                callbacks.onTimingWarning('download', 'Resource Timing API unavailable');
            }
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
        let timingSource;

        // For uploads, prefer Server-Timing header if available (most accurate)
        // Server-Timing measures server-side: time from request start to body fully received
        if (timing && timing.serverTiming && timing.serverTiming.length > 0) {
            const serverDur = timing.serverTiming.find(st => st.name === 'app');
            if (serverDur && serverDur.duration > 0) {
                durationMs = serverDur.duration;
                timingSource = 'server-timing';
                resourceTimingUsed = true;
            }
        }

        // Fallback to Resource Timing requestStart -> responseStart
        if (!durationMs && timing && timing.requestStart > 0 && timing.responseStart > 0) {
            durationMs = timing.responseStart - timing.requestStart;
            timingSource = 'resource-timing';
            resourceTimingUsed = true;
        }

        // Last fallback: manual timing
        if (!durationMs) {
            durationMs = manualEnd - manualStart;
            timingSource = 'manual';
            timingFallbackCount++;
            if (callbacks.onTimingWarning) {
                callbacks.onTimingWarning('upload', 'Resource Timing API unavailable');
            }
        }

        // Log first few uploads to verify timing source
        if (runIndex < 2 && profile !== 'warmup') {
            console.log(`Upload ${profile}/${runIndex}: ${timingSource}`, {
                durationMs: durationMs.toFixed(1),
                serverTiming: timing?.serverTiming?.map(st => ({ name: st.name, duration: st.duration }))
            });
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
            // Use ALL_*_PROFILES since dynamic profiles aren't set until estimation phase
            const downloadPromises = [];
            for (let i = 0; i < 6; i++) {
                downloadPromises.push(
                    runDownload(ALL_DOWNLOAD_PROFILES['100kB'].bytes, 'warmup', i)
                        .catch(() => {}) // Ignore individual failures
                );
            }
            await Promise.all(downloadPromises);

            // Run 6 parallel warmup uploads
            const uploadPromises = [];
            for (let i = 0; i < 6; i++) {
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
     * Run unloaded latency tests in parallel batches for speed
     */
    async function runUnloadedLatency() {
        const samples = [];
        const batchSize = 5; // Run 5 probes in parallel
        const totalProbes = CONFIG.latencyProbes;

        for (let batchStart = 0; batchStart < totalProbes; batchStart += batchSize) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            // Launch batch of probes in parallel
            const batchEnd = Math.min(batchStart + batchSize, totalProbes);
            const batchPromises = [];
            for (let i = batchStart; i < batchEnd; i++) {
                batchPromises.push(
                    runLatencyProbe('unloaded', i).catch(err => {
                        console.error(`Latency probe ${i} failed:`, err);
                        return null;
                    })
                );
            }

            const batchResults = await Promise.all(batchPromises);

            // Process results
            for (let i = 0; i < batchResults.length; i++) {
                const sample = batchResults[i];
                if (sample) {
                    samples.push(sample);
                    results.latencySamples.push(sample);
                }
            }

            // Report progress after each batch
            if (callbacks.onLatencyProgress) {
                const lastSample = batchResults.find(s => s) || { rttMs: 0 };
                callbacks.onLatencyProgress('unloaded', batchEnd, totalProbes, lastSample);
            }
        }
        return samples;
    }

    /**
     * Run download tests with adaptive profile selection
     * Runs baseline profiles first, then re-estimates before larger profiles
     */
    async function runDownloadTests() {
        let totalRuns = 0;
        const allSamples = [];

        // Phase 1: Run all 100kB tests (baseline, always included)
        const profile100k = ALL_DOWNLOAD_PROFILES['100kB'];
        const profile1m = ALL_DOWNLOAD_PROFILES['1MB'];
        const baselineRuns = profile100k.runs + profile1m.runs;

        console.log('Download: running 100kB baseline tests...');
        for (let i = 0; i < profile100k.runs; i++) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            try {
                const sample = await runDownload(profile100k.bytes, '100kB', i);
                allSamples.push(sample.mbps);
                results.throughputSamples.push(sample);
                totalRuns++;

                if (callbacks.onDownloadProgress) {
                    callbacks.onDownloadProgress('100kB', i + 1, profile100k.runs, sample, totalRuns, baselineRuns);
                }
            } catch (err) {
                console.error(`Download 100kB run ${i} failed:`, err);
            }
        }

        // Phase 2: Run all 1MB tests (baseline, always included)
        const samplesFor1MB = [];
        console.log('Download: running 1MB baseline tests...');
        for (let i = 0; i < profile1m.runs; i++) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            try {
                const sample = await runDownload(profile1m.bytes, '1MB', i);
                samplesFor1MB.push(sample.mbps);
                allSamples.push(sample.mbps);
                results.throughputSamples.push(sample);
                totalRuns++;

                if (callbacks.onDownloadProgress) {
                    callbacks.onDownloadProgress('1MB', i + 1, profile1m.runs, sample, totalRuns, baselineRuns);
                }
            } catch (err) {
                console.error(`Download 1MB run ${i} failed:`, err);
            }
        }

        // Phase 3: Estimate sustained speed from 1MB tests (after burst buffers depleted)
        const estimatedSpeed = estimateSpeed(samplesFor1MB);
        DOWNLOAD_PROFILES = selectDownloadProfiles(estimatedSpeed);
        console.log(`Download: estimated sustained speed ${estimatedSpeed.toFixed(1)} Mbps`);

        // Phase 4: Run larger profiles with time budget
        const largerProfiles = ['10MB', '25MB', '100MB', '250MB', '500MB', '1GB', '2GB', '5GB', '12GB', '50GB', '100GB', '125GB'];
        const phase4StartTime = performance.now();
        const timeBudgetMs = TOTAL_DOWNLOAD_DURATION_SECONDS * 1000;

        // Calculate expected total runs for progress reporting (baseline + selected larger profiles)
        let expectedTotal = baselineRuns;
        for (const name of largerProfiles) {
            if (DOWNLOAD_PROFILES[name]) {
                expectedTotal += DOWNLOAD_PROFILES[name].runs;
            }
        }

        for (const profileName of largerProfiles) {
            if (abortController?.signal.aborted) break;
            if (!DOWNLOAD_PROFILES[profileName]) continue;
            const { bytes, runs } = DOWNLOAD_PROFILES[profileName];

            // Check if entire batch can fit in remaining time budget
            const elapsedMs = performance.now() - phase4StartTime;
            const remainingMs = timeBudgetMs - elapsedMs;
            const estimatedBatchTime = estimateTransferTime(bytes, estimatedSpeed) * runs * 1000;

            if (estimatedBatchTime > remainingMs) {
                console.log(`Download: skipping ${profileName} batch (${runs} runs, ~${(estimatedBatchTime / 1000).toFixed(1)}s) - only ${(remainingMs / 1000).toFixed(1)}s remaining`);
                continue;
            }

            for (let run = 0; run < runs; run++) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);

                try {
                    const sample = await runDownload(bytes, profileName, run);
                    results.throughputSamples.push(sample);
                    totalRuns++;

                    if (callbacks.onDownloadProgress) {
                        callbacks.onDownloadProgress(profileName, run + 1, runs, sample, totalRuns, expectedTotal);
                    }
                } catch (err) {
                    console.error(`Download ${profileName} run ${run} failed:`, err);
                    if (callbacks.onError) {
                        callbacks.onError('download', profileName, run, err);
                    }
                }
            }
        }

        const totalElapsed = (performance.now() - phase4StartTime) / 1000;
        console.log(`Download: completed in ${totalElapsed.toFixed(1)}s`);
    }

    /**
     * Run upload tests with adaptive profile selection
     * Runs baseline profiles first, then re-estimates before larger profiles
     */
    async function runUploadTests() {
        let totalRuns = 0;
        const allSamples = [];

        // Phase 1: Run all 100kB tests (baseline, always included)
        const profile100k = ALL_UPLOAD_PROFILES['100kB'];
        const profile1m = ALL_UPLOAD_PROFILES['1MB'];
        const baselineRuns = profile100k.runs + profile1m.runs;

        console.log('Upload: running 100kB baseline tests...');
        for (let i = 0; i < profile100k.runs; i++) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            try {
                const sample = await runUpload(profile100k.bytes, '100kB', i);
                allSamples.push(sample.mbps);
                results.throughputSamples.push(sample);
                totalRuns++;

                if (callbacks.onUploadProgress) {
                    callbacks.onUploadProgress('100kB', i + 1, profile100k.runs, sample, totalRuns, baselineRuns);
                }
            } catch (err) {
                console.error(`Upload 100kB run ${i} failed:`, err);
            }
        }

        // Phase 2: Run all 1MB tests (baseline, always included)
        const samplesFor1MB = [];
        console.log('Upload: running 1MB baseline tests...');
        for (let i = 0; i < profile1m.runs; i++) {
            if (abortController?.signal.aborted) break;
            while (isPaused) await sleep(100);

            try {
                const sample = await runUpload(profile1m.bytes, '1MB', i);
                samplesFor1MB.push(sample.mbps);
                allSamples.push(sample.mbps);
                results.throughputSamples.push(sample);
                totalRuns++;

                if (callbacks.onUploadProgress) {
                    callbacks.onUploadProgress('1MB', i + 1, profile1m.runs, sample, totalRuns, baselineRuns);
                }
            } catch (err) {
                console.error(`Upload 1MB run ${i} failed:`, err);
            }
        }

        // Phase 3: Estimate sustained speed from 1MB tests (after burst buffers depleted)
        const estimatedSpeed = estimateSpeed(samplesFor1MB);
        UPLOAD_PROFILES = selectUploadProfiles(estimatedSpeed);
        console.log(`Upload: estimated sustained speed ${estimatedSpeed.toFixed(1)} Mbps`);

        // Phase 4: Run larger profiles with time budget
        const largerProfiles = ['10MB', '25MB', '50MB', '100MB', '250MB', '500MB', '1GB', '2GB', '5GB', '12GB', '50GB', '100GB', '125GB'];
        const phase4StartTime = performance.now();
        const timeBudgetMs = TOTAL_UPLOAD_DURATION_SECONDS * 1000;

        // Calculate expected total runs for progress reporting (baseline + selected larger profiles)
        let expectedTotal = baselineRuns;
        for (const name of largerProfiles) {
            if (UPLOAD_PROFILES[name]) {
                expectedTotal += UPLOAD_PROFILES[name].runs;
            }
        }

        for (const profileName of largerProfiles) {
            if (abortController?.signal.aborted) break;
            if (!UPLOAD_PROFILES[profileName]) continue;
            const { bytes, runs } = UPLOAD_PROFILES[profileName];

            // Check if entire batch can fit in remaining time budget
            const elapsedMs = performance.now() - phase4StartTime;
            const remainingMs = timeBudgetMs - elapsedMs;
            const estimatedBatchTime = estimateTransferTime(bytes, estimatedSpeed) * runs * 1000;

            if (estimatedBatchTime > remainingMs) {
                console.log(`Upload: skipping ${profileName} batch (${runs} runs, ~${(estimatedBatchTime / 1000).toFixed(1)}s) - only ${(remainingMs / 1000).toFixed(1)}s remaining`);
                continue;
            }

            for (let run = 0; run < runs; run++) {
                if (abortController?.signal.aborted) break;
                while (isPaused) await sleep(100);

                try {
                    const sample = await runUpload(bytes, profileName, run);
                    results.throughputSamples.push(sample);
                    totalRuns++;

                    if (callbacks.onUploadProgress) {
                        callbacks.onUploadProgress(profileName, run + 1, runs, sample, totalRuns, expectedTotal);
                    }
                } catch (err) {
                    console.error(`Upload ${profileName} run ${run} failed:`, err);
                    if (callbacks.onError) {
                        callbacks.onError('upload', profileName, run, err);
                    }
                }
            }
        }

        const totalElapsed = (performance.now() - phase4StartTime) / 1000;
        console.log(`Upload: completed in ${totalElapsed.toFixed(1)}s`);
    }

    /**
     * Get the largest available profile from a profiles object
     */
    function getLargestProfile(profiles, allProfiles) {
        // Prefer profiles that exist in the dynamic set, falling back to ALL_*_PROFILES
        const available = Object.keys(profiles).length > 0 ? profiles : allProfiles;
        let largest = null;
        let largestBytes = 0;
        for (const [name, cfg] of Object.entries(available)) {
            if (cfg.bytes > largestBytes) {
                largest = { name, bytes: cfg.bytes };
                largestBytes = cfg.bytes;
            }
        }
        return largest || { name: '1MB', bytes: 1000000 };
    }

    /**
     * Run latency probes during download
     */
    async function runLatencyDuringDownload() {
        // Use largest available profile to saturate connection
        const profile = getLargestProfile(DOWNLOAD_PROFILES, ALL_DOWNLOAD_PROFILES);
        const downloadPromise = runDownload(profile.bytes, profile.name, 0, 'download');

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
        // Use largest available profile to saturate connection
        const profile = getLargestProfile(UPLOAD_PROFILES, ALL_UPLOAD_PROFILES);
        const uploadPromise = runUpload(profile.bytes, profile.name, 0, 'upload');

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

            // Wait for data channel to open with ICE connection monitoring
            await new Promise((resolve, reject) => {
                const timeout = setTimeout(() => reject(new Error('ICE connection timeout')), 15000);

                // Monitor ICE connection state for failures
                pc.oniceconnectionstatechange = () => {
                    const state = pc.iceConnectionState;
                    if (state === 'failed') {
                        clearTimeout(timeout);
                        reject(new Error('ICE connection failed'));
                    } else if (state === 'disconnected') {
                        // Give it a moment to recover before failing
                        setTimeout(() => {
                            if (pc.iceConnectionState === 'disconnected' || pc.iceConnectionState === 'failed') {
                                clearTimeout(timeout);
                                reject(new Error('ICE connection disconnected'));
                            }
                        }, 2000);
                    }
                };

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
                        reject(new Error('Data channel error: ' + (err.message || 'unknown')));
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

            // Detect if this looks like a connection failure rather than actual packet loss
            // Check if responses suddenly stopped (connection died) vs random loss throughout
            let likelyConnectionIssue = false;
            let connectionIssueReason = '';

            if (received === 0) {
                // No responses at all - definitely a connection issue
                likelyConnectionIssue = true;
                connectionIssueReason = 'No responses received - connection failed';
            } else if (lossPercent > 10) {
                // Check the pattern: did responses stop after some point?
                // Get the highest sequence number that got an ack
                const ackedSeqs = Array.from(acks.keys()).sort((a, b) => a - b);
                const maxAckedSeq = ackedSeqs[ackedSeqs.length - 1];
                const minAckedSeq = ackedSeqs[0];

                // If we got acks for early packets but not late ones, connection likely died
                // Check what % of the last 20% of packets got acks
                const lateThreshold = Math.floor(sent * 0.8);
                const lateAcks = ackedSeqs.filter(seq => seq >= lateThreshold).length;
                const expectedLateAcks = sent - lateThreshold;
                const lateAckPercent = (lateAcks / expectedLateAcks) * 100;

                // If we got less than 50% of the late packets but more than 80% of early ones,
                // it's likely the connection died partway through
                const earlyAcks = ackedSeqs.filter(seq => seq < lateThreshold).length;
                const earlyAckPercent = (earlyAcks / lateThreshold) * 100;

                console.log('Packet loss pattern analysis:', {
                    earlyAckPercent: earlyAckPercent.toFixed(1) + '%',
                    lateAckPercent: lateAckPercent.toFixed(1) + '%',
                    maxAckedSeq,
                    totalSent: sent
                });

                if (earlyAckPercent > 80 && lateAckPercent < 50) {
                    likelyConnectionIssue = true;
                    connectionIssueReason = `Connection died mid-test - last response at packet ${maxAckedSeq}/${sent}`;
                } else if (lossPercent > 50) {
                    // Very high loss throughout - something is wrong
                    likelyConnectionIssue = true;
                    connectionIssueReason = `Connection unstable - received only ${received}/${sent} responses`;
                }
            }

            if (likelyConnectionIssue) {
                console.warn('Packet loss test:', connectionIssueReason);
                const unavailableResult = {
                    sent,
                    received,
                    lossPercent: 0,
                    rttStatsMs: { min: 0, median: 0, p90: 0 },
                    jitterMs: 0,
                    unavailable: true,
                    reason: connectionIssueReason
                };
                results.packetLoss = unavailableResult;

                // Clean up
                dc.close();
                pc.close();
                return unavailableResult;
            }

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

            // Collect data channel stats before closing
            results.dataChannelStats = await collectDataChannelStats(pc);

            // Analyze loss pattern
            results.lossPattern = analyzeLossPattern(sent, acks);

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

            // Determine reason for failure
            let reason = 'WebRTC connection failed';
            if (err.message?.includes('ICE connection timeout')) {
                reason = 'ICE connection timeout';
            } else if (err.message?.includes('ICE connection failed')) {
                reason = 'ICE connection failed';
            } else if (err.message?.includes('ICE connection disconnected')) {
                reason = 'ICE connection disconnected';
            } else if (err.message?.includes('ICE gathering timeout')) {
                reason = 'ICE gathering timeout';
            } else if (err.message?.includes('Data channel error')) {
                reason = 'Data channel error';
            } else if (err.message?.includes('offer failed')) {
                reason = 'Server rejected connection';
            }

            const unavailableResult = {
                sent: 0,
                received: 0,
                lossPercent: 0,
                rttStatsMs: { min: 0, median: 0, p90: 0 },
                jitterMs: 0,
                unavailable: true,
                reason: reason
            };
            results.packetLoss = unavailableResult;

            if (callbacks.onError) {
                callbacks.onError('packetLoss', null, null, err);
            }
            return unavailableResult;
        }
    }

    /**
     * Calculate summary statistics
     */
    function calculateSummary() {
        // Filter throughput samples by minimum duration for accurate timing.
        // Samples under 10ms are too short for reliable speed calculation
        // due to timing resolution limits.
        const MIN_DURATION_MS = 10;

        const dlSamples = results.throughputSamples
            .filter(s => s.direction === 'download' && s.durationMs >= MIN_DURATION_MS)
            .map(s => s.mbps);
        const ulSamples = results.throughputSamples
            .filter(s => s.direction === 'upload' && s.durationMs >= MIN_DURATION_MS)
            .map(s => s.mbps);

        console.log('Throughput filtering:', {
            dlTotal: results.throughputSamples.filter(s => s.direction === 'download').length,
            dlAfterFilter: dlSamples.length,
            ulTotal: results.throughputSamples.filter(s => s.direction === 'upload').length,
            ulAfterFilter: ulSamples.length
        });

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
        // Single pass to separate download/upload samples
        const dlSamples = [];
        const ulSamples = [];
        for (let i = 0; i < samples.length; i++) {
            const s = samples[i];
            if (s.direction === 'download') dlSamples.push(s.mbps);
            else if (s.direction === 'upload') ulSamples.push(s.mbps);
        }

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

        // Ensure we have valid numbers (default to safe values if NaN/undefined)
        const downloadMbps = summary.downloadMbps || 0;
        const latencyMs = isNaN(summary.latencyUnloadedMs) ? 50 : summary.latencyUnloadedMs;
        const jitterMs = isNaN(summary.jitterMs) ? 10 : summary.jitterMs;
        const packetLossPercent = isNaN(summary.packetLossPercent) ? 0 : summary.packetLossPercent;
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

    /**
     * Assess test confidence
     */
    function assessTestConfidence(samples, latency, packetLoss) {
        const warnings = [];

        // Single pass to collect samples by direction
        const dlMbps = [];
        const ulMbps = [];
        for (let i = 0; i < samples.length; i++) {
            const s = samples[i];
            if (s.direction === 'download') dlMbps.push(s.mbps);
            else if (s.direction === 'upload') ulMbps.push(s.mbps);
        }

        // Single pass for latency
        const latRtt = [];
        for (let i = 0; i < latency.length; i++) {
            if (latency[i].phase === 'unloaded') latRtt.push(latency[i].rttMs);
        }

        const dlCount = dlMbps.length;
        const ulCount = ulMbps.length;
        const latCount = latRtt.length;
        const sampleAdequate = dlCount >= 20 && ulCount >= 15 && latCount >= 10;
        if (!sampleAdequate) warnings.push('Insufficient samples for high confidence');

        // Coefficient of variation with single pass
        function cv(arr) {
            const n = arr.length;
            if (n < 2) return 0;
            let sum = 0;
            for (let i = 0; i < n; i++) sum += arr[i];
            const mean = sum / n;
            let sumSqDiff = 0;
            for (let i = 0; i < n; i++) {
                const diff = arr[i] - mean;
                sumSqDiff += diff * diff;
            }
            const std = Math.sqrt(sumSqDiff / n);
            return mean > 0 ? (std / mean) * 100 : 0;
        }

        const dlCV = cv(dlMbps);
        const ulCV = cv(ulMbps);
        const latCV = cv(latRtt);
        const cvAcceptable = dlCV < 30 && ulCV < 30 && latCV < 50;
        if (!cvAcceptable) warnings.push('High variability in measurements');

        // Connection stability
        const connectionStable = packetLoss !== null && !packetLoss.unavailable;
        if (!connectionStable) warnings.push('Packet loss test incomplete');

        // Timing accuracy
        const timingAccurate = resourceTimingUsed && timingFallbackCount < 5;
        if (!timingAccurate && timingFallbackCount > 0) {
            warnings.push('Some timing measurements used fallback methods');
        }

        // Overall score (4 factors: sample count, variability, connection stability, timing)
        let score = 100;
        if (!sampleAdequate) score -= 25;
        if (!cvAcceptable) score -= 35;
        if (!connectionStable) score -= 20;
        if (!timingAccurate) score -= 10;

        let overall;
        if (score >= 80) overall = 'high';
        else if (score >= 50) overall = 'medium';
        else overall = 'low';

        return {
            overall,
            overallScore: Math.max(0, score),
            metrics: {
                sampleCount: { download: dlCount, upload: ulCount, latency: latCount, adequate: sampleAdequate },
                coefficientOfVariation: { download: dlCV, upload: ulCV, latency: latCV, acceptable: cvAcceptable },
                timingAccuracy: {
                    resourceTimingUsed: resourceTimingUsed,
                    fallbackCount: timingFallbackCount,
                    accurate: timingAccurate
                },
                connectionStability: {
                    packetTestCompleted: connectionStable,
                    stable: connectionStable
                }
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
