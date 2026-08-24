'use strict';

const assert = require('node:assert/strict');
const SpeedTest = require('../../web/js/speedtest.js');

function resetMeasurementResults() {
    const results = SpeedTest.getResults();
    results.throughputSamples.length = 0;
    results.latencySamples.length = 0;
    results.packetLoss = null;
    return results;
}

{
    const results = resetMeasurementResults();
    let summary = SpeedTest.calculateSummary();
    assert.equal(summary.packetLossPercent, null,
        'skipped packet loss must remain unavailable rather than becoming zero');
    assert.deepEqual(SpeedTest.calculateQuality(summary), {
        videoStreaming: 'Incomplete',
        gaming: 'Incomplete',
        videoChatting: 'Incomplete'
    });

    results.packetLoss = {
        unavailable: true,
        lossPercent: null,
        sent: 0,
        received: 0
    };
    summary = SpeedTest.calculateSummary();
    assert.equal(summary.packetLossPercent, null,
        'failed packet loss must remain unavailable rather than becoming zero');

    results.packetLoss = {
        unavailable: false,
        lossPercent: 0,
        sent: 1000,
        received: 1000
    };
    summary = SpeedTest.calculateSummary();
    assert.equal(summary.packetLossPercent, 0,
        'a measured zero-loss result must remain distinguishable from unavailable');
}

console.log('speedtest browser summary tests passed');
