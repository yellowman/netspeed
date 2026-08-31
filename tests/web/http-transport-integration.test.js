'use strict';

const assert = require('node:assert/strict');

const resourceTimings = new Map();
let monotonicNow = 0;
Object.defineProperty(global, 'performance', {
    configurable: true,
    value: {
        now() {
            monotonicNow += 2;
            return monotonicNow;
        },
        getEntriesByName(url) {
            const entry = resourceTimings.get(url);
            return entry ? [entry] : [];
        }
    }
});

global.window = {
    location: {
        origin: 'http://test.local',
        href: 'http://test.local/app/index.html'
    }
};
global.NETSPEED_CONFIG = {};

const SpeedTest = require('../../web/js/speedtest.js');
const hooks = SpeedTest.__test;

function capabilities(overrides = {}) {
    return {
        version: 1,
        downloadPath: '/measure/down',
        downloadBytesParameter: 'n',
        downloadPayloadParameter: 'kind',
        downloadFramingParameter: 'frame',
        downloadChunkBytesParameter: 'chunk',
        downloadFlushParameter: 'flushNow',
        uploadPath: '/measure/up',
        uploadBytesParameter: 'n',
        httpPingPath: '/measure/ping',
        httpPingMethods: ['GET', 'HEAD'],
        warmConnectionPing: true,
        downloadPayloads: ['random', 'zero'],
        downloadFramings: ['fixed', 'chunked'],
        defaultDownloadPayload: 'random',
        defaultDownloadFraming: 'fixed',
        defaultChunkBytes: 64 * 1024,
        minimumChunkBytes: 4 * 1024,
        maximumChunkBytes: 1024 * 1024,
        uploadContentEncodings: ['identity'],
        responseCacheControl: 'no-store, no-transform',
        noTransform: true,
        proxyBufferSuppressionHeader: 'X-Accel-Buffering: no',
        proxyRequestBufferingAdvisory: true,
        ...overrides
    };
}

function strictHeaders(measurement, extras = {}) {
    return {
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': measurement,
        ...extras
    };
}

function absolute(url) {
    return new URL(url, global.window.location.href).href;
}

function requestHeaders(options) {
    return new Headers(options.headers || {});
}

function setTiming(url, overrides = {}) {
    resourceTimings.set(absolute(url), {
        fetchStart: 10,
        domainLookupStart: 10,
        domainLookupEnd: 10,
        connectStart: 10,
        connectEnd: 10,
        requestStart: 11,
        responseStart: 15,
        responseEnd: 25,
        nextHopProtocol: 'h2',
        ...overrides
    });
}

async function testNegotiatedDownloadAndUpload() {
    hooks.setServerCapabilities(1_000_000, 1);
    const selection = hooks.setMeasurementTransport(capabilities(), {
        downloadPayload: 'zero',
        downloadFraming: 'chunked',
        downloadChunkBytes: 4096,
        downloadFlush: false
    });
    assert.equal(selection.downloadPath, '/measure/down');
    assert.equal(selection.downloadPayload, 'zero');
    assert.equal(selection.downloadFraming, 'chunked');
    assert.equal(selection.downloadFlush, false);

    const requests = [];
    global.fetch = async (url, options = {}) => {
        requests.push({ url, options });
        setTiming(url);
        const parsed = new URL(url, global.window.location.href);
        if (parsed.pathname === '/measure/down') {
            assert.equal(parsed.searchParams.get('n'), '8192');
            assert.equal(parsed.searchParams.get('kind'), 'zero');
            assert.equal(parsed.searchParams.get('frame'), 'chunked');
            assert.equal(parsed.searchParams.get('chunk'), '4096');
            assert.equal(parsed.searchParams.get('flushNow'), 'false');
            assert.equal(parsed.searchParams.get('bytes'), null, 'hard-coded bytes key must not leak');

            const headers = requestHeaders(options);
            assert.equal(headers.get('Accept'), 'application/octet-stream');
            assert.equal(headers.get('Cache-Control'), 'no-store, no-transform');
            assert.equal(headers.get('Pragma'), 'no-cache');
            assert.equal(headers.get('Accept-Encoding'), null, 'script must not fake forbidden Accept-Encoding control');

            return new Response(new Uint8Array(8192), {
                status: 200,
                headers: strictHeaders('download', {
                    'Content-Type': 'application/octet-stream',
                    'X-Netspeed-Payload': 'zero',
                    'X-Netspeed-Framing': 'chunked',
                    'X-Netspeed-Chunk-Bytes': '4096',
                    'X-Netspeed-Flush': 'false'
                })
            });
        }
        if (parsed.pathname === '/measure/up') {
            assert.equal(parsed.searchParams.get('n'), '4096');
            assert.equal(parsed.searchParams.get('bytes'), null, 'hard-coded upload bytes key must not leak');
            assert.equal(options.method, 'POST');
            assert.equal(options.body.byteLength, 4096);

            const headers = requestHeaders(options);
            assert.equal(headers.get('Content-Type'), 'application/octet-stream');
            assert.equal(headers.get('Content-Encoding'), 'identity');
            assert.equal(headers.get('Cache-Control'), 'no-store, no-transform');
            assert.equal(headers.get('Pragma'), 'no-cache');

            return new Response(JSON.stringify({
                ok: true,
                acceptedBytes: 4096,
                serverDurationNs: 2_000_000
            }), {
                status: 200,
                headers: strictHeaders('upload', {
                    'Content-Type': 'application/json',
                    'X-Netspeed-Payload': 'discarded',
                    'X-Netspeed-Framing': 'fixed',
                    'X-Netspeed-Content-Encoding': 'identity',
                    'X-Netspeed-Expected-Bytes': '4096',
                    'X-Netspeed-Accepted-Bytes': '4096',
                    'X-Netspeed-Upload-Duration-Ns': '2000000'
                })
            });
        }
        throw new Error(`unexpected request ${url}`);
    };

    const download = await hooks.runDownload(8192, 'strict', 0);
    assert.equal(download.sizeBytes, 8192);
    assert.equal(download.transportPayload, 'zero');
    assert.equal(download.transportFraming, 'chunked');
    assert.equal(download.transportChunkBytes, 4096);
    assert.equal(download.transportFlush, false);
    assert.equal(download.payloadEvidence.verified, true);
    assert.equal(download.payloadEvidence.nonZeroBytes, 0);

    const upload = await hooks.runUpload(4096, 'strict', 0);
    assert.equal(upload.sizeBytes, 4096);
    assert.equal(upload.transportPayload, 'binary-zero');
    assert.equal(upload.transportFraming, 'fixed');
    assert.equal(upload.transportContentEncoding, 'identity');

    const evidence = hooks.getTransportEvidence();
    assert.equal(evidence.capabilitySource, 'measurementCapabilities');
    assert.equal(evidence.responseVerifications.download, 1);
    assert.equal(evidence.responseVerifications.upload, 1);
    assert.equal(evidence.responseVerifications.lastDownload.payload, 'zero');
    assert.equal(evidence.requestControls.acceptEncoding, 'browser-managed');
    assert.equal(requests.length, 2);
}

async function testXHRUploadPreservesTransportHeaders() {
    hooks.setServerCapabilities(1_000_000, 1);
    hooks.setMeasurementTransport(capabilities());

    const instances = [];
    class FakeXMLHttpRequest {
        constructor() {
            this.upload = {};
            this.requestHeaders = new Map();
            this.status = 0;
            this.statusText = '';
            this.response = null;
            instances.push(this);
        }

        open(method, url, asynchronous) {
            this.method = method;
            this.url = url;
            this.asynchronous = asynchronous;
        }

        setRequestHeader(name, value) {
            this.requestHeaders.set(name.toLowerCase(), value);
        }

        getAllResponseHeaders() {
            return Object.entries(strictHeaders('upload', {
                'Content-Type': 'application/json',
                'X-Netspeed-Payload': 'discarded',
                'X-Netspeed-Framing': 'fixed',
                'X-Netspeed-Content-Encoding': 'identity',
                'X-Netspeed-Expected-Bytes': '2048',
                'X-Netspeed-Accepted-Bytes': '2048',
                'X-Netspeed-Upload-Duration-Ns': '3000000'
            })).map(([name, value]) => `${name}: ${value}`).join('\r\n');
        }

        send(payload) {
            this.payload = payload;
            if (this.upload.onloadstart) this.upload.onloadstart();
            this.status = 200;
            this.statusText = 'OK';
            this.response = new TextEncoder().encode(JSON.stringify({
                ok: true,
                acceptedBytes: 2048,
                serverDurationNs: 3_000_000
            })).buffer;
            queueMicrotask(() => {
                if (this.onload) this.onload();
                if (this.onloadend) this.onloadend();
            });
        }

        abort() {
            if (this.onabort) this.onabort();
            if (this.onloadend) this.onloadend();
        }
    }

    const activity = {
        beginCalls: 0,
        endCalls: 0,
        begin(accurate) {
            this.beginCalls++;
            assert.equal(accurate, true);
            return Symbol('upload-activity');
        },
        end(token) {
            this.endCalls++;
            assert.equal(typeof token, 'symbol');
        }
    };

    global.XMLHttpRequest = FakeXMLHttpRequest;
    try {
        const sample = await hooks.runUpload(
            2048,
            'xhr-strict',
            1,
            'download',
            activity,
            new Uint8Array(2048)
        );
        assert.equal(sample.sizeBytes, 2048);
        assert.equal(sample.durationMs, 3);
    } finally {
        delete global.XMLHttpRequest;
    }

    assert.equal(instances.length, 1);
    const xhr = instances[0];
    const parsed = new URL(xhr.url, global.window.location.href);
    assert.equal(xhr.method, 'POST');
    assert.equal(xhr.asynchronous, true);
    assert.equal(parsed.pathname, '/measure/up');
    assert.equal(parsed.searchParams.get('n'), '2048');
    assert.equal(parsed.searchParams.get('during'), 'download');
    assert.equal(xhr.requestHeaders.get('content-type'), 'application/octet-stream');
    assert.equal(xhr.requestHeaders.get('content-encoding'), 'identity');
    assert.equal(xhr.requestHeaders.get('cache-control'), 'no-store, no-transform');
    assert.equal(xhr.requestHeaders.get('pragma'), 'no-cache');
    assert.equal(xhr.payload.byteLength, 2048);
    assert.equal(activity.beginCalls, 1);
    assert.equal(activity.endCalls, 1);
}

async function testResponseContractRejections() {
    hooks.setServerCapabilities(1_000_000, 1);
    hooks.setMeasurementTransport(capabilities(), {
        downloadPayload: 'zero',
        downloadFraming: 'fixed',
        downloadChunkBytes: 4096,
        downloadFlush: false
    });

    const baseHeaders = strictHeaders('download', {
        'Content-Type': 'application/octet-stream',
        'Content-Length': '4096',
        'X-Netspeed-Payload': 'zero',
        'X-Netspeed-Framing': 'fixed',
        'X-Netspeed-Chunk-Bytes': '4096',
        'X-Netspeed-Flush': 'false'
    });

    global.fetch = async url => {
        setTiming(url);
        return new Response(new Uint8Array(4096), {
            status: 200,
            headers: { ...baseHeaders, 'Content-Encoding': 'gzip' }
        });
    };
    await assert.rejects(hooks.runDownload(4096, 'strict', 0), /unsupported Content-Encoding/);

    global.fetch = async url => {
        setTiming(url);
        return new Response(new Uint8Array(4096), {
            status: 200,
            headers: { ...baseHeaders, 'Cache-Control': 'no-store' }
        });
    };
    await assert.rejects(hooks.runDownload(4096, 'strict', 0), /does not preserve no-store, no-transform/);

    global.fetch = async url => {
        setTiming(url);
        return new Response(new Uint8Array(4096), {
            status: 200,
            headers: { ...baseHeaders, 'X-Accel-Buffering': 'yes' }
        });
    };
    await assert.rejects(hooks.runDownload(4096, 'strict', 0), /X-Accel-Buffering/);

    const corrupted = new Uint8Array(8192);
    corrupted[7000] = 1;
    global.fetch = async url => {
        setTiming(url);
        return new Response(corrupted, {
            status: 200,
            headers: { ...baseHeaders, 'Content-Length': '8192' }
        });
    };
    await assert.rejects(hooks.runDownload(8192, 'strict', 0), /zero-fill download contained/);

    hooks.setMeasurementTransport(capabilities(), {
        downloadPayload: 'random',
        downloadFraming: 'fixed',
        downloadChunkBytes: 4096,
        downloadFlush: false
    });
    const compressible = new Uint8Array(4096).fill(1);
    global.fetch = async url => {
        setTiming(url);
        return new Response(compressible, {
            status: 200,
            headers: strictHeaders('download', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '4096',
                'X-Netspeed-Payload': 'random',
                'X-Netspeed-Framing': 'fixed',
                'X-Netspeed-Chunk-Bytes': '4096',
                'X-Netspeed-Flush': 'false'
            })
        });
    };
    await assert.rejects(hooks.runDownload(4096, 'strict', 0), /distinct byte values/);
}

async function testWarmLatencyDiscardAndEvidence() {
    hooks.setServerCapabilities(1_000_000, 1);
    hooks.setMeasurementTransport(capabilities({ httpPingMethods: ['HEAD'] }));

    let requestCount = 0;
    global.fetch = async (url, options = {}) => {
        requestCount++;
        assert.equal(options.method, 'HEAD');
        const parsed = new URL(url, global.window.location.href);
        assert.equal(parsed.pathname, '/measure/ping');
        assert.equal(parsed.searchParams.get('seq'), '7');
        assert.equal(requestHeaders(options).get('Cache-Control'), 'no-store, no-transform');

        if (requestCount === 1) {
            setTiming(url, {
                connectStart: 10,
                connectEnd: 15,
                requestStart: 15,
                responseStart: 20,
                responseEnd: 20.5
            });
        } else {
            setTiming(url, {
                connectStart: 30,
                connectEnd: 30,
                requestStart: 31,
                responseStart: 35,
                responseEnd: 35.5
            });
        }
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };

    const sample = await hooks.runLatencyProbe('unloaded', 7);
    assert.equal(requestCount, 2, 'one cold probe should be discarded before a warm sample is accepted');
    assert.equal(sample.connectionReused, true);
    assert.equal(sample.connectionSetupMs, 0);
    assert.equal(sample.probeTransport, 'http');
    assert.equal(sample.probeMethod, 'HEAD');
    assert.equal(sample.probePath, '/measure/ping');
    assert.equal(sample.nextHopProtocol, 'h2');
    assert.equal(sample.rttMs, 4);

    const evidence = hooks.getTransportEvidence();
    assert.equal(evidence.latency.discardedColdAttempts, 1);
    assert.equal(evidence.latency.verifiedReusedSamples, 1);
    assert.deepEqual(evidence.latency.nextHopProtocols, ['h2']);
}

async function testWarmLatencyRejectsOnlyColdAttempts() {
    hooks.setServerCapabilities(1_000_000, 1);
    hooks.setMeasurementTransport(capabilities({ httpPingMethods: ['GET'] }));

    let requestCount = 0;
    global.fetch = async url => {
        requestCount++;
        setTiming(url, {
            connectStart: 10 * requestCount,
            connectEnd: (10 * requestCount) + 3,
            requestStart: (10 * requestCount) + 3,
            responseStart: (10 * requestCount) + 5,
            responseEnd: (10 * requestCount) + 5.5
        });
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };

    await assert.rejects(
        hooks.runLatencyProbe('download', 3),
        /incurred connection setup after 3 probes/
    );
    assert.equal(requestCount, 3);
    assert.equal(hooks.getTransportEvidence().latency.discardedColdAttempts, 3);
}

async function testUnobservableReuseIsNotMislabeled() {
    hooks.setServerCapabilities(1_000_000, 1);
    hooks.setMeasurementTransport(capabilities());

    global.fetch = async url => {
        setTiming(url, {
            connectStart: 0,
            connectEnd: 0,
            requestStart: 11,
            responseStart: 14,
            responseEnd: 14.5,
            nextHopProtocol: ''
        });
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };

    const sample = await hooks.runLatencyProbe('upload', 4);
    assert.equal(sample.connectionReused, null);
    assert.equal(sample.connectionSetupMs, null);
    assert.equal(sample.connectionReuseEvidence, 'resource-timing-connection-fields-unavailable');
    assert.equal(sample.connectionSetupExcluded, true);
    const evidence = hooks.getTransportEvidence();
    assert.equal(evidence.latency.unobservableReuseSamples, 1);
    assert.equal(evidence.latency.verifiedReusedSamples, 0);
}


async function testUnverifiableSetupIsRejected() {
    hooks.setServerCapabilities(1_000_000, 1);
    hooks.setMeasurementTransport(capabilities());

    let requestCount = 0;
    global.fetch = async url => {
        requestCount++;
        setTiming(url, {
            connectStart: 0,
            connectEnd: 0,
            requestStart: 0,
            responseStart: 13,
            responseEnd: 14,
            fetchStart: 10,
            nextHopProtocol: ''
        });
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };

    await assert.rejects(
        hooks.runLatencyProbe('unloaded', 9),
        /could not exclude connection setup after 3 probes/
    );
    assert.equal(requestCount, 3);
    const evidence = hooks.getTransportEvidence();
    assert.equal(evidence.latency.discardedUnverifiableAttempts, 3);
    assert.equal(evidence.latency.unobservableReuseSamples, 0);
}


function websocketCapabilities(overrides = {}) {
    return capabilities({
        webSocketPingPath: '/measure/ws',
        webSocketPingProtocol: 'netspeed.ping.v1',
        webSocketPingPayloadBytes: 16,
        ...overrides
    });
}

async function testWebSocketLatencyPreferredAndPersistent() {
    hooks.setServerCapabilities(1_000_000, 1);
    let connections = 0;
    let messages = 0;
    class FakeWebSocket {
        constructor(url, protocol) {
            this.url = url;
            this.protocol = protocol;
            this.readyState = 0;
            connections++;
            setImmediate(() => {
                this.readyState = 1;
                this.onopen?.({});
            });
        }
        send(payload) {
            messages++;
            const copy = new Uint8Array(payload).slice();
            setImmediate(() => this.onmessage?.({ data: copy.buffer }));
        }
        close(code = 1000, reason = '') {
            if (this.readyState === 3) return;
            this.readyState = 3;
            setImmediate(() => this.onclose?.({ code, reason }));
        }
    }
    global.WebSocket = FakeWebSocket;
    let fetches = 0;
    global.fetch = async () => {
        fetches++;
        throw new Error('HTTP fallback must not run for a healthy WebSocket');
    };

    const selection = hooks.setMeasurementTransport(websocketCapabilities());
    assert.equal(selection.preferredLatencyTransport, 'websocket');
    const first = await hooks.runLatencyProbe('unloaded', 1);
    const second = await hooks.runLatencyProbe('download', 2);
    for (const sample of [first, second]) {
        assert.equal(sample.probeTransport, 'websocket');
        assert.equal(sample.probeMethod, 'MESSAGE');
        assert.equal(sample.probePath, '/measure/ws');
        assert.equal(sample.timingSource, 'websocket-message');
        assert.equal(sample.connectionReused, true);
        assert.equal(sample.connectionSetupExcluded, true);
        assert.equal(sample.webSocketProtocol, 'netspeed.ping.v1');
    }
    assert.equal(connections, 1);
    assert.equal(messages, 3, 'one warmup and two measured messages expected');
    assert.equal(fetches, 0);
    const evidence = hooks.getTransportEvidence();
    assert.equal(evidence.latency.preferredTransport, 'websocket');
    assert.equal(evidence.latency.activeTransport, 'websocket');
    assert.equal(evidence.latency.webSocket.connections, 1);
    assert.equal(evidence.latency.webSocket.warmups, 1);
    assert.equal(evidence.latency.webSocket.successfulPings, 2);
    assert.equal(evidence.latency.fallbackUsed, false);
    hooks.closeWebSocketLatency();
}

async function testWebSocketFailurePermanentlyFallsBackToHTTP() {
    hooks.setServerCapabilities(1_000_000, 1);
    let connections = 0;
    class FailingWebSocket {
        constructor() {
            this.protocol = '';
            this.readyState = 0;
            connections++;
            setImmediate(() => {
                this.onerror?.({});
                this.readyState = 3;
                this.onclose?.({ code: 1006, reason: 'blocked by proxy' });
            });
        }
        close() { this.readyState = 3; }
    }
    global.WebSocket = FailingWebSocket;
    let fetches = 0;
    global.fetch = async url => {
        fetches++;
        setTiming(url);
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };

    hooks.setMeasurementTransport(websocketCapabilities());
    const first = await hooks.runLatencyProbe('unloaded', 1);
    const second = await hooks.runLatencyProbe('upload', 2);
    for (const sample of [first, second]) {
        assert.equal(sample.probeTransport, 'http');
        assert.equal(sample.connectionReused, true);
        assert.match(sample.probeFallbackReason, /WebSocket/i);
    }
    assert.equal(connections, 1, 'failed WebSocket must not be retried for every sample');
    assert.equal(fetches, 2);
    const evidence = hooks.getTransportEvidence();
    assert.equal(evidence.latency.activeTransport, 'http');
    assert.equal(evidence.latency.fallbackUsed, true);
    assert.match(evidence.latency.fallbackReason, /WebSocket/i);
    assert.equal(evidence.latency.webSocket.disabled, true);
    hooks.closeWebSocketLatency();
}

async function testCrossOriginSameOriginCredentialsSkipBrowserWebSocket() {
    hooks.setServerCapabilities(1_000_000, 1);
    global.NETSPEED_CONFIG = {
        apiBaseUrl: 'https://speed-api.example.test/',
        credentials: 'same-origin'
    };
    let connections = 0;
    global.WebSocket = class {
        constructor() { connections++; }
    };
    global.fetch = async url => {
        setTiming(url);
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };
    hooks.setMeasurementTransport(websocketCapabilities());
    const sample = await hooks.runLatencyProbe('unloaded', 1);
    assert.equal(sample.probeTransport, 'http');
    assert.match(sample.probeFallbackReason, /same-origin.*cross-origin/);
    assert.equal(connections, 0);
    global.NETSPEED_CONFIG = {};
    hooks.closeWebSocketLatency();
}

async function testBearerTokenSkipsBrowserWebSocket() {
    hooks.setServerCapabilities(1_000_000, 1);
    global.NETSPEED_CONFIG = { accessToken: 'secret' };
    let connections = 0;
    global.WebSocket = class {
        constructor() { connections++; }
    };
    global.fetch = async url => {
        setTiming(url);
        return new Response(null, {
            status: 200,
            headers: strictHeaders('latency', {
                'Content-Type': 'application/octet-stream',
                'Content-Length': '0'
            })
        });
    };
    hooks.setMeasurementTransport(websocketCapabilities());
    const sample = await hooks.runLatencyProbe('unloaded', 1);
    assert.equal(sample.probeTransport, 'http');
    assert.match(sample.probeFallbackReason, /bearer token/);
    assert.equal(connections, 0);
    global.NETSPEED_CONFIG = {};
    hooks.closeWebSocketLatency();
}

async function main() {
    await testNegotiatedDownloadAndUpload();
    await testXHRUploadPreservesTransportHeaders();
    await testResponseContractRejections();
    await testWarmLatencyDiscardAndEvidence();
    await testWarmLatencyRejectsOnlyColdAttempts();
    await testUnobservableReuseIsNotMislabeled();
    await testUnverifiableSetupIsRejected();
    await testWebSocketLatencyPreferredAndPersistent();
    await testWebSocketFailurePermanentlyFallsBackToHTTP();
    await testCrossOriginSameOriginCredentialsSkipBrowserWebSocket();
    await testBearerTokenSkipsBrowserWebSocket();
    delete global.WebSocket;
    hooks.setMeasurementTransport(null);
    console.log('browser HTTP transport integration tests passed');
}

main().catch(error => {
    console.error(error);
    process.exitCode = 1;
});
