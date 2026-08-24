'use strict';

const assert = require('node:assert/strict');
const SpeedTest = require('./speedtest.js');

function resetResults() {
    const results = SpeedTest.getResults();
    results.meta = null;
    results.throughputSamples.length = 0;
    results.latencySamples.length = 0;
    results.packetLoss = null;
    return results;
}

async function main() {
    {
        const results = resetResults();
        results.packetLoss = {
            sent: 0,
            received: 0,
            lossPercent: null,
            unavailable: true,
            reason: 'not measured'
        };

        const summary = SpeedTest.calculateSummary();
        assert.equal(summary.downloadMbps, null);
        assert.equal(summary.uploadMbps, null);
        assert.equal(summary.latencyUnloadedMs, null);
        assert.equal(summary.jitterMs, null);
        assert.equal(summary.packetLossPercent, null);

        const quality = SpeedTest.calculateQuality(summary);
        assert.deepEqual(quality, {
            videoStreaming: 'N/A',
            gaming: 'N/A',
            videoChatting: 'N/A'
        });

        const exported = JSON.parse(SpeedTest.exportResults());
        assert.equal(exported.summary.packetLossPercent, null);
    }

    {
        const results = resetResults();
        results.throughputSamples.push(
            { direction: 'download', durationMs: 20, mbps: 100 },
            { direction: 'upload', durationMs: 20, mbps: 50 }
        );
        results.latencySamples.push(
            { phase: 'unloaded', rttMs: 10 },
            { phase: 'unloaded', rttMs: 10 },
            { phase: 'unloaded', rttMs: 10 },
            { phase: 'unloaded', rttMs: 10 }
        );
        results.packetLoss = {
            sent: 100,
            received: 100,
            lossPercent: 0,
            unavailable: false
        };

        const summary = SpeedTest.calculateSummary();
        assert.equal(summary.downloadMbps, 100);
        assert.equal(summary.uploadMbps, 50);
        assert.equal(summary.latencyUnloadedMs, 10);
        assert.equal(summary.jitterMs, 0);
        assert.equal(summary.packetLossPercent, 0);

        const quality = SpeedTest.calculateQuality(summary);
        assert.deepEqual(quality, {
            videoStreaming: 'Great',
            gaming: 'Great',
            videoChatting: 'Great'
        });
    }

    {
        const durationMs = SpeedTest.validateUploadReceipt({
            ok: true,
            acceptedBytes: 4096,
            serverDurationNs: 2_500_000
        }, 4096);
        assert.equal(durationMs, 2.5);

        assert.throws(() => SpeedTest.validateUploadReceipt({
            ok: true,
            acceptedBytes: 4095,
            serverDurationNs: 2_500_000
        }, 4096), /Invalid upload receipt/);
        assert.throws(() => SpeedTest.validateUploadReceipt({
            ok: true,
            acceptedBytes: 4096,
            serverDurationNs: 0
        }, 4096), /Invalid upload receipt/);
    }

    {
        const originalFetch = global.fetch;
        try {
            global.fetch = async () => ({
                ok: true,
                json: async () => ({ measurementApiVersion: 1, maxTransferBytes: 1_073_741_824 })
            });
            await SpeedTest.fetchMeta();
            assert.deepEqual(SpeedTest.getMeasurementCapabilities(), {
                measurementApiVersion: 1,
                serverMaxTransferBytes: 1_073_741_824,
                downloadMaxBytes: 100_000_000,
                uploadMaxBytes: 50_000_000
            });

            global.fetch = async () => ({
                ok: true,
                json: async () => ({ measurementApiVersion: 1 })
            });
            await assert.rejects(() => SpeedTest.fetchMeta(), /valid maxTransferBytes/);

            global.fetch = async () => ({
                ok: true,
                json: async () => ({ measurementApiVersion: 1, maxTransferBytes: 99_999 })
            });
            await assert.rejects(() => SpeedTest.fetchMeta(), /below the smallest browser profile/);

            // Reusing the module against a legacy server must clear the previous
            // version negotiation rather than treating a legacy response as v1.
            global.fetch = async () => ({
                ok: true,
                json: async () => ({ maxTransferBytes: 10_000_000 })
            });
            await SpeedTest.fetchMeta();
            assert.deepEqual(SpeedTest.getMeasurementCapabilities(), {
                measurementApiVersion: 0,
                serverMaxTransferBytes: 10_000_000,
                downloadMaxBytes: 10_000_000,
                uploadMaxBytes: 10_000_000
            });

            global.fetch = async () => ({
                ok: true,
                json: async () => ({})
            });
            await SpeedTest.fetchMeta();
            assert.deepEqual(SpeedTest.getMeasurementCapabilities(), {
                measurementApiVersion: 0,
                serverMaxTransferBytes: 100_000_000,
                downloadMaxBytes: 100_000_000,
                uploadMaxBytes: 50_000_000
            });
        } finally {
            global.fetch = originalFetch;
        }
    }
}

const originalLog = console.log;
console.log = () => {};
main().then(() => {
    console.log = originalLog;
    originalLog('speedtest phase-1 tests passed');
}).catch(err => {
    console.log = originalLog;
    console.error(err);
    process.exitCode = 1;
});
