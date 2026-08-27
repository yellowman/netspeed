'use strict';

const assert = require('node:assert/strict');
const SpeedTest = require('../../web/js/speedtest.js');
const hooks = SpeedTest.__test;

const changes = [];
SpeedTest.setCallbacks({
    onStageChange(change) {
        changes.push({ ...change });
    }
});

hooks.resetStageOutcomes();
hooks.emitStageChange('meta', 'running');
hooks.emitStageChange('meta', 'succeeded');
hooks.emitStageChange('packet-loss', 'running');
hooks.emitStageChange('packet-loss', 'unavailable', { reason: 'frame capability missing' });
hooks.emitStageChange('complete', 'running');
hooks.emitStageChange('complete', 'succeeded');

let outcomes = hooks.getStageOutcomes();
assert.equal(outcomes.meta.outcome, 'succeeded');
assert.equal(outcomes['packet-loss'].outcome, 'unavailable');
assert.equal(outcomes['packet-loss'].reason, 'frame capability missing');
assert.equal(outcomes.complete.outcome, 'succeeded');
assert.ok(changes.some(change => change.stage === 'packet-loss' && change.outcome === 'unavailable'));

hooks.resetStageOutcomes();
hooks.emitStageChange('download', 'running');
hooks.emitStageChange('loaded-latency', 'running');
hooks.failRunningStages(new Error('deliberate transfer failure'));
outcomes = hooks.getStageOutcomes();
assert.equal(outcomes.download.outcome, 'failed');
assert.equal(outcomes['loaded-latency'].outcome, 'failed');
assert.equal(outcomes['packet-loss'].outcome, 'pending');
assert.equal(outcomes.complete.outcome, 'failed');

console.log('structured browser stage outcomes validated');
