'use strict';

const assert = require('node:assert/strict');
global.window = global.window || { location: { origin: 'http://test.local' } };
global.NETSPEED_CONFIG = { accessToken: 'browser-token-012345' };
const SpeedTest = require('../../web/js/speedtest.js');
const hooks = SpeedTest.__test;

async function testAuthenticationHeaders() {
    let captured;
    global.fetch = async (_url, options = {}) => {
        captured = options.headers || {};
        return new Response(JSON.stringify({ maxTransferBytes: 1_000_000 }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' }
        });
    };
    await hooks.fetchMeta();
    assert.equal(captured.Authorization, 'Bearer browser-token-012345');

    global.NETSPEED_CONFIG.accessToken = '   ';
    await hooks.fetchMeta();
    assert.equal(captured.Authorization, undefined);
}

function testServerConcurrencyNegotiation() {
    hooks.setServerCapabilities(1 << 30, 1, 2, 1, 3);
    const limited = hooks.selectWindowPlan(1_000_000, 1 << 30, 'download');
    assert.equal(limited.concurrency, 2, 'one of three client slots must remain available for the loaded probe');

    hooks.setServerCapabilities(1 << 30, 1, 2, 1, 2);
    const minimum = hooks.selectWindowPlan(1_000_000, 1 << 30, 'upload');
    assert.equal(minimum.concurrency, 1);
}

async function main() {
    await testAuthenticationHeaders();
    testServerConcurrencyNegotiation();
    console.log('browser phase 4 hardening tests passed');
}

main().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
