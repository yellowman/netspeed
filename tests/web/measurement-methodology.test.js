'use strict';

const assert = require('node:assert/strict');
global.window = global.window || { location: { origin: 'http://test.local' } };
const SpeedTest = require('../../web/js/speedtest.js');
const hooks = SpeedTest.__test;

function closeEnough(got, want, epsilon = 1e-9) {
    assert.ok(Math.abs(got - want) < epsilon, `${got} is not within ${epsilon} of ${want}`);
}

async function testExactPacketFrames() {
    const probe = hooks.encodePacketFrame(42, 1_700_000_000_123, false, 0);
    assert.equal(probe.byteLength, 1200);
    assert.deepEqual(hooks.decodePacketFrame(probe), {
        acknowledgement: false,
        sequence: 42,
        sentAtUnixMilli: 1_700_000_000_123,
        recvAtUnixMilli: 0
    });

    const acknowledgement = hooks.encodePacketFrame(42, 1_700_000_000_123, true, 1_700_000_000_125);
    assert.equal(acknowledgement.byteLength, 1200);
    assert.deepEqual(hooks.decodePacketFrame(acknowledgement), {
        acknowledgement: true,
        sequence: 42,
        sentAtUnixMilli: 1_700_000_000_123,
        recvAtUnixMilli: 1_700_000_000_125
    });

    const corrupt = acknowledgement.slice();
    corrupt[1199] ^= 0xff;
    assert.throws(() => hooks.decodePacketFrame(corrupt), /Corrupt packet-loss padding/);
    assert.throws(() => hooks.decodePacketFrame(corrupt.subarray(0, 1199)), /frame size 1199/);
}

async function testSharedStatistics() {
    closeEnough(hooks.percentile([1, 2, 3, 4, 100], 90), 61.6);
    assert.deepEqual(
        hooks.prepareLatency([100, 80, 10, 11, 12, 13, 14, 2000], 2),
        [10, 11, 12, 13, 14]
    );
    closeEnough(hooks.calculateJitter([10, 20, 30]), 8);
    closeEnough(hooks.coefficientOfVariation([10, 20, 30]), 40.8248290463863);
}

async function testBoundedWindowPlans() {
    hooks.setServerCapabilities(1 << 30, 1, 2, 1);
    hooks.resetRequestStreamingSupport();
    const download = hooks.selectWindowPlan(1_000_000, 1 << 30, 'download');
    assert.equal(download.chunkBytes, 256 * 1024 * 1024);
    assert.equal(download.concurrency, 5);
    assert.equal(download.windows, 3);
    assert.equal(download.loadedWindow, 1);

    const originalReadableStream = global.ReadableStream;
    try {
        global.ReadableStream = undefined;
        hooks.resetRequestStreamingSupport();
        const upload = hooks.selectWindowPlan(1_000_000, 1 << 30, 'upload');
        assert.equal(upload.chunkBytes, 8 * 1024 * 1024);
        assert.equal(upload.concurrency, 5);
    } finally {
        global.ReadableStream = originalReadableStream;
        hooks.resetRequestStreamingSupport();
    }

    const limited = hooks.selectWindowPlan(1_000_000, 750_000, 'download', true);
    assert.equal(limited.chunkBytes, 750_000);
    assert.equal(limited.windows, 1);
    assert.equal(limited.loadedWindow, 0);
    assert.equal(limited.loadedProbeCount, 3);
}

async function testStreamingUploadBodyIsExactAndBounded() {
    const activity = hooks.createLoadActivity();
    const descriptor = hooks.createStreamingUploadBody((64 * 1024) + 17, activity);
    const reader = descriptor.body.getReader();
    let received = 0;
    let maximumChunk = 0;
    while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        received += value.byteLength;
        maximumChunk = Math.max(maximumChunk, value.byteLength);
    }
    assert.equal(received, (64 * 1024) + 17);
    assert.ok(maximumChunk <= 64 * 1024);
    assert.equal(activity.snapshot().active, 1, 'request remains active until receipt completion');
    descriptor.finish();
    assert.equal(activity.snapshot().active, 0);
}

async function testLoadedProbeRejectsGapSpanningSample() {
    const activity = hooks.createLoadActivity();
    let token = activity.begin(true);
    let calls = 0;
    const probe = async condition => {
        calls++;
        if (calls === 1) {
            activity.end(token);
            token = activity.begin(true);
        }
        return {
            ts: Date.now(),
            startedAt: Date.now() - 1,
            endedAt: Date.now(),
            rttMs: 10 + calls,
            condition,
            timingSource: 'resource-timing',
            loadOverlapped: false
        };
    };

    try {
        const samples = await hooks.runLoadedLatencyProbes('download', 1, activity, probe);
        assert.equal(samples.length, 1);
        assert.equal(samples[0].loadOverlapped, true);
        assert.equal(samples[0].loadTrackingAccurate, true);
        assert.ok(calls >= 2, 'gap-spanning first probe must be rejected and retried');
    } finally {
        activity.end(token);
    }
}

async function testLoadedProbeAcceptsQuorumWhenWindowStops() {
    const activity = hooks.createLoadActivity();
    const token = activity.begin(true);
    let calls = 0;
    let stopped = false;
    const probe = async condition => {
        calls++;
        if (calls === 3) stopped = true;
        return {
            ts: Date.now(),
            startedAt: Date.now() - 1,
            endedAt: Date.now(),
            rttMs: 10 + calls,
            condition,
            timingSource: 'resource-timing',
            loadOverlapped: false
        };
    };

    try {
        const samples = await hooks.runLoadedLatencyProbes(
            'download',
            5,
            activity,
            probe,
            () => stopped
        );
        assert.equal(samples.length, 3);
        assert.equal(calls, 3);
    } finally {
        activity.end(token);
    }
}

async function testWindowRepeatsOnlyBoundedRequests() {
    hooks.setServerCapabilities(1_000_000, 1, 2, 1);
    let active = 0;
    let maximumActive = 0;
    let requests = 0;
    const chunkBytes = 64 * 1024;
    const originalFetch = global.fetch;

    global.fetch = async url => {
        const parsed = new URL(url, 'http://test.local');
        assert.equal(Number(parsed.searchParams.get('bytes')), chunkBytes);
        active++;
        maximumActive = Math.max(maximumActive, active);
        requests++;
        await new Promise(resolve => setTimeout(resolve, 2));
        active--;
        return new Response(new Uint8Array(chunkBytes), {
            status: 200,
            headers: {
                'Content-Type': 'application/octet-stream',
                'Content-Length': String(chunkBytes)
            }
        });
    };

    try {
        const { sample, probes } = await hooks.runThroughputWindow('download', {
            chunkBytes,
            concurrency: 3,
            windowDurationMs: 50,
            windows: 1,
            loadedWindow: -1,
            loadedProbeCount: 0,
            direction: 'download'
        }, 0, false);
        assert.deepEqual(probes, []);
        assert.equal(sample.sampleKind, 'window');
        assert.equal(sample.chunkBytes, chunkBytes);
        assert.equal(sample.concurrency, 3);
        assert.equal(sample.requestCount, requests);
        assert.equal(sample.sizeBytes, requests * chunkBytes);
        assert.ok(requests > 3, 'workers should repeat requests without per-request timing lookup stalls');
        assert.ok(maximumActive <= 3);
        assert.equal(sample.timingSource, 'aggregate-wall-clock');
    } finally {
        global.fetch = originalFetch;
    }
}


async function testPacketReportConsistencyAndDirectionalConfidence() {
    hooks.validatePacketReport({
        forwardReceived: 995,
        acknowledgementsSent: 990,
        ackSendFailures: 5,
        duplicateFrames: 1,
        invalidFrames: 2
    }, 1000, 988);

    assert.throws(() => hooks.validatePacketReport({
        forwardReceived: 995,
        acknowledgementsSent: 990,
        ackSendFailures: 4,
        duplicateFrames: 0,
        invalidFrames: 0
    }, 1000, 988), /accounting is inconsistent/);

    const throughput = [100, 101, 102].map((mbps, index) => ({
        direction: 'download', mbps, durationMs: 1000, sampleKind: 'window',
        profile: 'window', windowIndex: index, timingSource: 'aggregate-wall-clock'
    }));
    const latency = [];
    for (let index = 0; index < 12; index++) {
        latency.push({ condition: 'unloaded', rttMs: 10 + (index % 2), timingSource: 'resource-timing' });
    }
    for (let index = 0; index < 3; index++) {
        latency.push({
            condition: 'download', rttMs: 15 + index, loadOverlapped: true,
            loadTrackingAccurate: true, timingSource: 'resource-timing'
        });
    }
    const forwardOnly = {
        unavailable: false,
        sent: 1000,
        received: 0,
        forwardLossPercent: 100,
        acknowledgementsSent: 0,
        reverseAcknowledgementLossPercent: null
    };
    const confidence = hooks.assessTestConfidence(throughput, latency, forwardOnly, { uploadExpected: false });
    assert.equal(confidence.metrics.packetTest.completed, false);
    assert.equal(confidence.overallScore, 80);
}

async function testSummaryAndConfidenceParity() {
    const throughputSamples = [
        { direction: 'download', mbps: 5000, durationMs: 100, sampleKind: 'baseline', profile: '1MB' },
        { direction: 'download', mbps: 100, durationMs: 1000, sampleKind: 'window', profile: 'window', timingSource: 'aggregate-wall-clock' },
        { direction: 'download', mbps: 110, durationMs: 1000, sampleKind: 'window', profile: 'window', timingSource: 'aggregate-wall-clock' },
        { direction: 'download', mbps: 120, durationMs: 1000, sampleKind: 'window', profile: 'window', timingSource: 'aggregate-wall-clock' },
        { direction: 'upload', mbps: 50, durationMs: 1000, sampleKind: 'window', profile: 'window', timingSource: 'aggregate-wall-clock' },
        { direction: 'upload', mbps: 60, durationMs: 1000, sampleKind: 'window', profile: 'window', timingSource: 'aggregate-wall-clock' },
        { direction: 'upload', mbps: 70, durationMs: 1000, sampleKind: 'window', profile: 'window', timingSource: 'aggregate-wall-clock' }
    ];
    const latencySamples = [
        { condition: 'unloaded', rttMs: 100, timingSource: 'resource-timing' },
        { condition: 'unloaded', rttMs: 200, timingSource: 'resource-timing' },
        { condition: 'unloaded', rttMs: 10, timingSource: 'resource-timing' },
        { condition: 'unloaded', rttMs: 20, timingSource: 'resource-timing' },
        { condition: 'unloaded', rttMs: 30, timingSource: 'resource-timing' },
        { condition: 'download', rttMs: 10, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' },
        { condition: 'download', rttMs: 20, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' },
        { condition: 'download', rttMs: 30, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' },
        { condition: 'download', rttMs: 10000, loadOverlapped: false, timingSource: 'resource-timing' },
        { condition: 'upload', rttMs: 20, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' },
        { condition: 'upload', rttMs: 30, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' },
        { condition: 'upload', rttMs: 40, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' }
    ];
    const packetLoss = {
        unavailable: false,
        sent: 1000,
        received: 988,
        transactionLossPercent: 1.2,
        forwardSent: 1000,
        forwardReceived: 995,
        forwardLossPercent: 0.5,
        acknowledgementsSent: 995,
        acknowledgementsReceived: 988,
        reverseAcknowledgementLossPercent: (7 / 995) * 100
    };

    hooks.setResults({ throughputSamples, latencySamples, packetLoss });
    const summary = hooks.calculateSummary();
    closeEnough(summary.downloadMbps, 118);
    closeEnough(summary.uploadMbps, 68);
    closeEnough(summary.latencyUnloadedMs, 20);
    closeEnough(summary.jitterMs, 8);
    closeEnough(summary.latencyDownloadMs, 28);
    closeEnough(summary.latencyUploadMs, 38);
    closeEnough(summary.packetLossPercent, 1.2);

    // Add enough low-variance unloaded samples for all five confidence gates.
    const highLatency = [];
    for (let index = 0; index < 12; index++) {
        highLatency.push({ condition: 'unloaded', rttMs: 10 + (index % 2), timingSource: 'resource-timing' });
    }
    for (let index = 0; index < 3; index++) {
        highLatency.push(
            { condition: 'download', rttMs: 15 + index, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' },
            { condition: 'upload', rttMs: 16 + index, loadOverlapped: true, loadTrackingAccurate: true, timingSource: 'resource-timing' }
        );
    }
    const confidence = hooks.assessTestConfidence(throughputSamples.filter(sample => sample.sampleKind === 'window'), highLatency, packetLoss);
    assert.equal(confidence.overall, 'high');
    assert.equal(confidence.overallScore, 100);
    assert.equal(confidence.metrics.sampleCount.adequate, true);
    assert.equal(confidence.metrics.loadedOverlap.complete, true);
    assert.equal(confidence.metrics.timingAccuracy.accurate, true);
    assert.equal(confidence.metrics.packetTest.completed, true);
}

async function main() {
    await testExactPacketFrames();
    await testSharedStatistics();
    await testBoundedWindowPlans();
    await testStreamingUploadBodyIsExactAndBounded();
    await testLoadedProbeRejectsGapSpanningSample();
    await testLoadedProbeAcceptsQuorumWhenWindowStops();
    await testWindowRepeatsOnlyBoundedRequests();
    await testPacketReportConsistencyAndDirectionalConfidence();
    await testSummaryAndConfidenceParity();
    console.log('browser measurement methodology tests passed');
}

main().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
