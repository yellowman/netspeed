'use strict';
const assert = require('node:assert/strict');
const Evidence = require('../../web/js/evidence.js');
const SpeedTest = require('../../web/js/speedtest.js');
const Charts = require('../../web/js/charts.js');
for (const width of [24, 36, 80, 132]) {
    const trace = Charts.terminalTrace([0, 100, 250, 500], width);
    assert.ok(trace.includes('*'));
    assert.ok(trace.split('\n').every(line => line.length <= width), 'character trace respects its column budget');
    const distribution = Charts.terminalBoxPlot([10, 12, 14, 16], width);
    assert.equal(distribution.stats.median, 13);
    assert.ok(distribution.text.includes('+'));
}

const input = {
    meta: { futureCapability: { enabled: false, value: 0, note: '<script>never execute</script>' } },
    latencySamples: [
        { condition: 'unloaded', rttMs: null, timingResolutionLimited: true },
        { condition: 'unloaded', rttMs: 12, connectionReused: true, probeTransport: 'websocket' },
        { condition: 'unloaded', rttMs: 14, connectionReused: true, probeTransport: 'websocket' },
        { condition: 'download', rttMs: 25, loadOverlapped: true, connectionReused: true },
        { condition: 'download', rttMs: 999, loadOverlapped: false }
    ],
    discardedLatencySamples: [{ condition: 'download', rttMs: 30, discardReason: 'cold-connection' }],
    dataChannelStats: { connectionType: 'relay' },
    packetLoss: { unavailable: true, reason: 'TURN relay unavailable' },
    httpTransport: { latency: { probeTransport: 'websocket', fallbackUsed: true, fallbackReason: 'echo timeout' } },
    futureTelemetry: [{ zero: 0, missing: null, disabled: false }]
};
const saved = Evidence.snapshot(input);
assert.deepEqual(saved, input, 'unknown nested telemetry, zero, false, and null remain intact');
saved.meta.futureCapability.value = 10;
assert.equal(input.meta.futureCapability.value, 0, 'snapshot must not alias a live result');
assert.equal(Evidence.latencyEvidence(input, 'unloaded').median, 13);
assert.equal(Evidence.latencyEvidence(input, 'unloaded').belowResolution, 1);
assert.equal(Evidence.latencyEvidence(input, 'download').median, 25, 'non-overlapping probes are not observations of loaded latency');
assert.equal(Evidence.latencyEvidence(input, 'download').discarded, 1);
assert.equal(Evidence.latencyEvidence(input, 'upload').median, null, 'empty condition has no fabricated latency');
const notes = Evidence.measurementNotes(input).join('\n');
assert.match(notes, /timer resolution limited 1/);
assert.match(notes, /TURN relay unavailable/);
assert.match(notes, /rather than a direct path/);
assert.match(notes, /echo timeout/);
assert.deepEqual(Evidence.measurementNotes(null), []);
assert.deepEqual(Evidence.measurementNotes({}), []);
assert.ok(Evidence.fields(input.meta).some(([key, value]) => key.endsWith('enabled') && value === 'false'));

const result = SpeedTest.getResults();
Object.assign(result, input, { lossPattern: { type: 'burst', maxBurstLength: 2 }, locations: [{ city: 'Redmond' }] });
const exported = JSON.parse(SpeedTest.exportResults());
for (const key of ['futureTelemetry', 'meta', 'latencySamples', 'discardedLatencySamples', 'dataChannelStats', 'lossPattern', 'locations']) assert.deepEqual(exported[key], result[key], `export preserves ${key}`);
assert.ok(exported.stageOutcomes, 'export includes structured stage outcomes');
console.log('lossless evidence workspace contracts passed');
