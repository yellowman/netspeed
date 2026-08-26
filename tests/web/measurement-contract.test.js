'use strict';

const assert = require('node:assert/strict');

let clock = 0;
Object.defineProperty(global, 'performance', {
    configurable: true,
    value: {
        now() {
            clock += 5;
            return clock;
        },
        getEntriesByName() {
            return [{
                requestStart: 1,
                responseStart: 2,
                responseEnd: 12,
                fetchStart: 0.5
            }];
        }
    }
});
global.window = { location: { origin: 'http://test.local' } };

const SpeedTest = require('../../web/js/speedtest.js');
const hooks = SpeedTest.__test;
hooks.setServerCapabilities(1_000_000, 1);

async function expectReject(promise, pattern) {
    await assert.rejects(promise, pattern);
}

async function main() {
    global.fetch = async () => new Response(JSON.stringify({
        maxTransferBytes: 1_000_000,
        uploadReceiptVersion: 1
    }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
    });
    const meta = await hooks.fetchMeta();
    assert.equal(meta.maxTransferBytes, 1_000_000);

    global.fetch = async () => new Response('not json', {
        status: 200,
        headers: { 'Content-Type': 'text/plain' }
    });
    await expectReject(hooks.fetchMeta(), /unexpected content type/);

    global.fetch = async () => new Response(' '.repeat((1024 * 1024) + 1), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
    });
    await expectReject(hooks.fetchMeta(), /exceeds 1048576 bytes/);

    global.fetch = async () => new Response(JSON.stringify([{ iata: 'TST' }]), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
    });
    const locations = await hooks.fetchLocations();
    assert.deepEqual(locations, [{ iata: 'TST' }]);

    global.fetch = async () => new Response(new Uint8Array([1, 2, 3, 4]), {
        status: 200,
        headers: {
            'Content-Type': 'application/octet-stream',
            'Content-Length': '4'
        }
    });
    const download = await hooks.runDownload(4, 'test', 0);
    assert.equal(download.sizeBytes, 4);
    assert.equal(download.durationMs, 10);
    assert.equal(download.timingSource, 'resource-timing');

    global.fetch = async () => new Response(new Uint8Array([1, 2]), {
        status: 200,
        headers: {
            'Content-Type': 'application/octet-stream',
            'Content-Length': '2'
        }
    });
    await expectReject(hooks.runDownload(4, 'test', 0), /Content-Length 2; expected 4/);

    global.fetch = async () => new Response(new Uint8Array([1, 2, 3, 4, 5]), {
        status: 200,
        headers: { 'Content-Type': 'application/octet-stream' }
    });
    await expectReject(hooks.runDownload(4, 'test', 0), /received 5 bytes; expected 4/);

    global.fetch = async () => new Response('broken', {
        status: 500,
        headers: { 'Content-Type': 'text/plain' }
    });
    await expectReject(hooks.runDownload(4, 'test', 0), /HTTP 500: broken/);

    global.fetch = async (_url, options) => {
        assert.equal(options.method, 'POST');
        assert.equal(options.body.byteLength, 8);
        return new Response(JSON.stringify({
            ok: true,
            acceptedBytes: 8,
            serverDurationNs: 2_000_000
        }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' }
        });
    };
    const upload = await hooks.runUpload(8, 'test', 0);
    assert.equal(upload.sizeBytes, 8);
    assert.equal(upload.durationMs, 2);
    assert.equal(upload.timingSource, 'server-receipt');

    const uploadActivity = hooks.createLoadActivity();
    let activeAfterRequestBody = -1;
    global.fetch = async (_url, options) => {
        const reader = options.body.getReader();
        let received = 0;
        while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            received += value.byteLength;
        }
        activeAfterRequestBody = uploadActivity.snapshot().active;
        return new Response(JSON.stringify({
            ok: true,
            acceptedBytes: received,
            serverDurationNs: 2_000_000
        }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' }
        });
    };
    const trackedUpload = await hooks.runUpload(
        8,
        'tracked',
        0,
        'upload',
        uploadActivity,
        activity => hooks.createStreamingUploadBody(8, activity)
    );
    assert.equal(trackedUpload.sizeBytes, 8);
    assert.equal(activeAfterRequestBody, 1, 'upload stays active while awaiting its verified receipt');
    assert.equal(uploadActivity.snapshot().active, 0, 'upload activity ends after receipt completion');

    global.fetch = async () => new Response(JSON.stringify({
        ok: true,
        acceptedBytes: 7,
        serverDurationNs: 2_000_000
    }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
    });
    await expectReject(hooks.runUpload(8, 'test', 0), /accepted 7 upload bytes; expected 8/);

    global.fetch = async () => new Response(
        '{"ok":true,"acceptedBytes":8,"serverDurationNs":2000000} {"extra":true}',
        {
            status: 200,
            headers: { 'Content-Type': 'application/json' }
        }
    );
    await expectReject(hooks.runUpload(8, 'test', 0), /Invalid upload receipt/);

    global.fetch = async () => new Response(' '.repeat((64 * 1024) + 1), {
        status: 200,
        headers: { 'Content-Type': 'application/json' }
    });
    await expectReject(hooks.runUpload(8, 'test', 0), /exceeds 65536 bytes/);

    hooks.setServerCapabilities(4, 1);
    await expectReject(hooks.runDownload(5, 'test', 0), /exceeds negotiated maximum/);
    await expectReject(hooks.runUpload(5, 'test', 0), /exceeds browser maximum/);

    hooks.setServerCapabilities(1_000_000, 0);
    await expectReject(hooks.runUpload(1, 'test', 0), /verified upload receipts/);

    const originalReadableStream = global.ReadableStream;
    global.ReadableStream = undefined;
    hooks.setServerCapabilities(200_000_000, 1);
    await expectReject(hooks.runDownload(150_000_000, 'test', 0), /exceeds negotiated maximum 100000000/);
    global.ReadableStream = originalReadableStream;

    console.log('browser measurement contract tests passed');
}

main().catch(err => {
    console.error(err);
    process.exitCode = 1;
});
