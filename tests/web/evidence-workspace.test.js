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

const windows = {
    summary: { downloadMbps: 42, uploadMbps: 12 },
    throughputSamples: [
        { direction: 'download', sampleKind: 'window', sizeBytes: 1000, durationMs: 1500, concurrency: 4, chunkBytes: 65536, mbps: 42 },
        { direction: 'upload', sampleKind: 'window', sizeBytes: 200, durationMs: 1200, concurrency: 2, mbps: 12 },
        { direction: 'download', sampleKind: 'baseline', sizeBytes: 500, durationMs: 100, concurrency: 1 }
    ]
};
assert.equal(Evidence.windowSamples(windows).length, 2, 'acquisition ledger contains actual load windows only');
const downloadEvidence = Object.fromEntries(Evidence.throughputEvidence(windows, 'download'));
assert.equal(downloadEvidence['Sustained Mbps'], 42);
assert.equal(downloadEvidence['Observed samples'], 2);
assert.equal(downloadEvidence['Load windows'], 1);
assert.equal(downloadEvidence['Payload bytes across windows'], 1000);
assert.equal(downloadEvidence['Last window ms'], 1500, 'trailing baseline must not replace window timing');
assert.equal(downloadEvidence.Workers, 4);
assert.equal(Object.fromEntries(Evidence.throughputEvidence({ throughputSamples: [{ direction: 'download', sampleKind: 'window' }] }, 'download'))['Payload bytes across windows'], null, 'unknown bytes must not be credited as zero');
assert.deepEqual(Evidence.windowSamples(null), []);
const emptySelection = Object.fromEntries(Evidence.throughputEvidence({
    throughputSamples: [{ direction: 'download', sampleKind: 'window', sizeBytes: 0 }],
    httpTransport: { selection: { '': false } }
}, 'download'));
assert.equal(emptySelection['Payload bytes across windows'], 0, 'an observed zero is not missing data');
assert.equal(emptySelection[''], 'false', 'unknown empty-named selection fields cannot break inspection');

assert.deepEqual(Evidence.measurementSignature(null), []);
assert.deepEqual(Evidence.measurementSignature({ sharedResult: true, startTime: 0, endTime: 100 }), [], 'shared links must not acquire invented transport provenance');
const signature = Evidence.measurementSignature({ ...windows, latencySamples: input.latencySamples, startTime: 0, endTime: 1500, httpTransport: {
    selection: { downloadPayload: 'random', downloadFraming: 'chunked' },
    latency: { probeTransport: 'websocket', verifiedReusedSamples: 2 }
} });
assert.ok(signature.includes('WS reused'));
assert.ok(signature.includes('random/chunked selected'), 'selection must be identified as selected, not remotely observed');
assert.ok(signature.includes('DL 4 flows') && signature.includes('UL 2 flows'), 'unequal directions cannot imply one concurrency');
assert.ok(signature.includes('1.5 s'), 'duration is the actual start/end interval');
const branches = Evidence.connectionBranches({ ...windows, meta: { serverName: '<img>' }, latencySamples: [{ condition: 'unloaded', rttMs: 12, probeTransport: 'websocket', connectionReused: false }], httpTransport: { latency: { probeTransport: 'websocket' } }, packetLoss: { lossPercent: 0 }, dataChannelStats: { connectionType: 'relay' } });
assert.equal(branches.length, 3);
assert.equal(branches[0].link, 'HTTP throughput');
assert.equal(branches[1].link, 'WebSocket RTT');
assert.equal(branches[2].link, 'TURN relay');
assert.equal(branches[2].to, '0.00% loss', 'zero loss is evidence, not absence');
assert.equal(Evidence.connectionBranches({ packetLoss: { unavailable: true, reason: 'TURN unavailable' } })[0].to, 'TURN unavailable');
assert.deepEqual(Evidence.connectionBranches(null), []);

const fallback = {
    httpTransport: { latency: { probeTransport: 'http', fallbackUsed: true, fallbackReason: 'echo timeout', verifiedReusedSamples: 1 } },
    latencySamples: [
        { condition: 'unloaded', rttMs: 12, probeTransport: 'websocket', connectionReused: true },
        { condition: 'unloaded', rttMs: 14, probeTransport: 'http', connectionReused: null, connectionSetupExcluded: true }
    ]
};
assert.deepEqual(Evidence.measurementSignature(fallback), ['WS reused → HTTP (reuse unobserved) (fallback)']);
assert.equal(Evidence.connectionBranches(fallback)[0].link, 'WebSocket RTT / reused → HTTP RTT / reuse unobserved (fallback)');
assert.doesNotMatch(Evidence.measurementSignature(fallback).join(' '), /HTTP reused/);
assert.doesNotMatch(Evidence.connectionBranches(fallback)[0].link, /HTTP RTT \/ reused/);
assert.deepEqual(Evidence.measurementSignature({ ...fallback, latencySamples: [...fallback.latencySamples].reverse() }), Evidence.measurementSignature(fallback), 'fallback order comes from the recorded fallback, not condition-grouped array order');
const httpReused = Evidence.snapshot(fallback);
httpReused.latencySamples[1].connectionReused = true;
httpReused.httpTransport.latency.verifiedReusedSamples = 0;
assert.deepEqual(Evidence.measurementSignature(httpReused), ['WS reused → HTTP reused (fallback)'], 'each transport uses its own sample evidence, not the aggregate counter');
assert.match(Evidence.connectionBranches(httpReused)[0].link, /HTTP RTT \/ reused/);
const mixed = Evidence.snapshot(fallback);
mixed.httpTransport.latency.fallbackUsed = false;
assert.deepEqual(Evidence.measurementSignature(mixed), ['WS reused + HTTP (reuse unobserved) (mixed RTT)'], 'mixed transports do not imply a fallback sequence without evidence');
assert.doesNotMatch(Evidence.connectionBranches(mixed)[0].link, /→|fallback/);
const excluded = {
    httpTransport: fallback.httpTransport,
    latencySamples: [
        { condition: 'unloaded', rttMs: null, rawRttMs: 0, timingResolutionLimited: true, probeTransport: 'http', connectionReused: true },
        { condition: 'download', rttMs: 12, loadOverlapped: false, probeTransport: 'http', connectionReused: true },
        { condition: 'unloaded', rttMs: 0, probeTransport: 'http', connectionReused: true },
        { condition: 'unloaded', rttMs: 12, connectionReused: true }
    ],
    discardedLatencySamples: [{ condition: 'unloaded', rttMs: 12, probeTransport: 'http', connectionReused: true }]
};
assert.deepEqual(Evidence.measurementSignature(excluded), [], 'excluded, discarded, and unscoped samples cannot establish transport or reuse');
assert.deepEqual(Evidence.connectionBranches(excluded), []);
assert.deepEqual(Evidence.measurementSignature({ httpTransport: fallback.httpTransport }), [], 'final transport metadata alone does not prove an accepted observation');
assert.deepEqual(Evidence.connectionBranches({ httpTransport: fallback.httpTransport }), []);
assert.deepEqual(Evidence.measurementSignature({ ...fallback, latencySamples: [fallback.latencySamples[0]] }), ['WS reused'], 'final HTTP metadata cannot imply an accepted HTTP sample');
const httpOnly = { ...fallback, latencySamples: [fallback.latencySamples[1]] };
assert.deepEqual(Evidence.measurementSignature(httpOnly), ['HTTP (reuse unobserved) / fallback used'], 'an attempted but unmeasured WebSocket must not become an observed path');
assert.equal(Evidence.connectionBranches(httpOnly)[0].link, 'HTTP RTT / reuse unobserved / fallback used');
const loadedHttp = { ...httpOnly, latencySamples: [{ ...httpOnly.latencySamples[0], condition: 'download', loadOverlapped: true, connectionReused: true }] };
assert.deepEqual(Evidence.measurementSignature(loadedHttp), ['HTTP reused / fallback used'], 'verified overlapping loaded samples also establish their own transport reuse');
assert.deepEqual(fallback.latencySamples.map(s => s.connectionReused), [true, null], 'presentation does not alter sample provenance');

assert.equal(Evidence.sampleStrip({ sampleKind: 'window', mbps: 0, bytes: 0, requestCount: 0 }, 1, 3), 'window 2/3 · 0.0 Mbps · 0.0 MB · 0 req');
assert.equal(Evidence.sampleStrip({ sampleKind: 'window' }, 0, 1), 'window 1/1', 'unknown bytes, workers, and requests are not guessed');
assert.doesNotMatch(Evidence.eventDescription({ type: 'latency', condition: 'download', rttMs: 0, timingResolutionLimited: true }), /RTT 0/);
assert.match(Evidence.eventDescription({ type: 'latency', rttMs: 12.6, transport: 'websocket', connectionReused: true, loadOverlapped: true }), /WS echo.*reused connection.*RTT 12.6 ms.*overlap verified/);
assert.match(Evidence.eventDescription({ type: 'stage', stage: 'packet-loss', outcome: 'unavailable', reason: 'TURN unavailable' }), /Packet path unavailable.*TURN unavailable/);

const result = SpeedTest.getResults();
Object.assign(result, input, { lossPattern: { type: 'burst', maxBurstLength: 2 }, locations: [{ city: 'Redmond' }] });
const exported = JSON.parse(SpeedTest.exportResults());
for (const key of ['futureTelemetry', 'meta', 'latencySamples', 'discardedLatencySamples', 'dataChannelStats', 'lossPattern', 'locations']) assert.deepEqual(exported[key], result[key], `export preserves ${key}`);
assert.ok(exported.stageOutcomes, 'export includes structured stage outcomes');
console.log('lossless evidence workspace contracts passed');
