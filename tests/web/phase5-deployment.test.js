'use strict';

const assert = require('node:assert/strict');

global.window = {
    location: {
        origin: 'https://ui.example.test',
        href: 'https://ui.example.test/app/index.html'
    }
};
global.NETSPEED_CONFIG = {
    apiBaseUrl: 'https://speed.example.test/netspeed-api',
    accessToken: 'browser-token-012345',
    credentials: 'include'
};

const SpeedTest = require('../../web/js/speedtest.js');
const hooks = SpeedTest.__test;

async function testAbsoluteAPIBaseAndCredentialMode() {
    assert.equal(hooks.apiURL('/meta'), 'https://speed.example.test/netspeed-api/meta');
    assert.equal(hooks.apiURL('/__down?bytes=0'), 'https://speed.example.test/netspeed-api/__down?bytes=0');
    assert.equal(hooks.requestCredentialsMode(), 'include');

    let capturedURL;
    let capturedOptions;
    global.fetch = async (url, options = {}) => {
        capturedURL = url;
        capturedOptions = options;
        return new Response(JSON.stringify({ maxTransferBytes: 1_000_000 }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' }
        });
    };
    await hooks.fetchMeta();
    assert.equal(capturedURL, 'https://speed.example.test/netspeed-api/meta');
    assert.equal(capturedOptions.credentials, 'include');
    assert.equal(capturedOptions.headers.Authorization, 'Bearer browser-token-012345');
}

function testRelativeBaseAndSafeDefaults() {
    global.NETSPEED_CONFIG = { apiBaseUrl: '/measurement', includeCredentials: false };
    assert.equal(hooks.apiURL('/locations'), 'https://ui.example.test/measurement/locations');
    assert.equal(hooks.requestCredentialsMode(), 'same-origin');

    global.NETSPEED_CONFIG = {};
    assert.equal(hooks.apiURL('/meta'), '/meta');
    assert.equal(hooks.requestCredentialsMode(), 'same-origin');

    global.NETSPEED_CONFIG = { credentials: 'omit' };
    assert.equal(hooks.xhrCanHonorCredentials('/__up'), false, 'same-origin XHR cannot implement omit');
    global.NETSPEED_CONFIG = { apiBaseUrl: 'https://speed.example.test/', credentials: 'omit' };
    assert.equal(hooks.xhrCanHonorCredentials(hooks.apiURL('/__up')), true, 'cross-origin XHR withCredentials=false implements omit');
}

function testInvalidBaseIsRejected() {
    global.NETSPEED_CONFIG = { apiBaseUrl: 'ftp://speed.example.test/' };
    assert.throws(() => hooks.apiURL('/meta'), /must use http or https/);

    global.NETSPEED_CONFIG = { apiBaseUrl: 'https://user:pass@speed.example.test/' };
    assert.throws(() => hooks.apiURL('/meta'), /cannot contain credentials/);
}

async function main() {
    await testAbsoluteAPIBaseAndCredentialMode();
    testRelativeBaseAndSafeDefaults();
    testInvalidBaseIsRejected();
    console.log('browser phase 5 deployment tests passed');
}

main().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
