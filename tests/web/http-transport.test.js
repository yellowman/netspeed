'use strict';

const assert = require('node:assert/strict');
const Transport = require('../../web/js/http_transport.js');

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
        httpPingMethods: ['HEAD', 'GET'],
        webSocketPingPath: '',
        warmConnectionPing: true,
        downloadPayloads: ['random', 'zero'],
        downloadFramings: ['fixed', 'chunked'],
        defaultDownloadPayload: 'random',
        defaultDownloadFraming: 'fixed',
        defaultChunkBytes: 65536,
        minimumChunkBytes: 4096,
        maximumChunkBytes: 1048576,
        uploadContentEncodings: ['identity'],
        responseCacheControl: 'no-store, no-transform',
        noTransform: true,
        proxyBufferSuppressionHeader: 'X-Accel-Buffering: no',
        proxyRequestBufferingAdvisory: true,
        ...overrides
    };
}

function strictSelection(overrides = {}) {
    return Transport.negotiate(capabilities(), {
        downloadPayload: 'zero',
        downloadFraming: 'chunked',
        downloadChunkBytes: 4096,
        downloadFlush: false,
        ...overrides
    });
}

function response(body, headers) {
    return new Response(body, { status: 200, headers });
}

{
    const validated = Transport.validateCapabilities(capabilities());
    assert.equal(validated.preferredPingMethod, 'GET');
    assert.deepEqual(validated.proxyBufferSuppressionRequirement, {
        name: 'X-Accel-Buffering', value: 'no'
    });

    const selection = strictSelection();
    assert.deepEqual(selection, {
        capabilityVersion: 1,
        legacyFallback: false,
        downloadPath: '/measure/down',
        downloadBytesParameter: 'n',
        downloadPayloadParameter: 'kind',
        downloadFramingParameter: 'frame',
        downloadChunkBytesParameter: 'chunk',
        downloadFlushParameter: 'flushNow',
        downloadPayload: 'zero',
        downloadFraming: 'chunked',
        downloadChunkBytes: 4096,
        downloadFlush: false,
        uploadPath: '/measure/up',
        uploadBytesParameter: 'n',
        uploadContentEncoding: 'identity',
        latencyPath: '/measure/ping',
        latencyMethod: 'GET',
        latencyUsesDownloadEndpoint: false,
        warmConnectionPing: true,
        noTransform: true,
        responseCacheControl: 'no-store, no-transform',
        proxyBufferSuppressionHeader: 'X-Accel-Buffering: no',
        proxyRequestBufferingAdvisory: true,
        webSocketPingPath: '',
        webSocketPingProtocol: '',
        webSocketPingPayloadBytes: 0,
        preferredLatencyTransport: 'http',
        httpFallbackAvailable: true
    });

    const download = new URL(Transport.buildDownloadPath(selection, 8192, {
        measId: 'download-id', profile: 'test'
    }), 'https://speed.example.test');
    assert.equal(download.pathname, '/measure/down');
    assert.deepEqual(Object.fromEntries(download.searchParams), {
        measId: 'download-id',
        profile: 'test',
        n: '8192',
        kind: 'zero',
        frame: 'chunked',
        chunk: '4096',
        flushNow: 'false'
    });

    const upload = new URL(Transport.buildUploadPath(selection, 2048, { measId: 'upload-id' }), 'https://speed.example.test');
    assert.equal(upload.pathname, '/measure/up');
    assert.equal(upload.searchParams.get('n'), '2048');
    assert.equal(upload.searchParams.get('bytes'), null);

    assert.deepEqual(Transport.buildLatencyRequest(selection, { seq: 7 }), {
        method: 'GET',
        path: '/measure/ping?seq=7'
    });
}

{
    const websocket = Transport.negotiate(capabilities({
        webSocketPingPath: '/measure/ws',
        webSocketPingProtocol: 'netspeed.ping.v1',
        webSocketPingPayloadBytes: 16
    }), {});
    assert.equal(websocket.preferredLatencyTransport, 'websocket');
    assert.equal(websocket.webSocketPingPath, '/measure/ws');
    assert.equal(websocket.webSocketPingProtocol, Transport.WEBSOCKET_PING_PROTOCOL);
    assert.equal(websocket.webSocketPingPayloadBytes, Transport.WEBSOCKET_PING_PAYLOAD_BYTES);
    assert.equal(websocket.httpFallbackAvailable, true);
    assert.throws(() => Transport.validateCapabilities(capabilities({
        webSocketPingPath: '/measure/ws'
    })), /unsupported WebSocket ping protocol/);
    assert.throws(() => Transport.validateCapabilities(capabilities({
        webSocketPingProtocol: 'netspeed.ping.v1',
        webSocketPingPayloadBytes: 16
    })), /without webSocketPingPath/);
}

{
    const legacy = Transport.negotiate(null, {});
    assert.equal(legacy.legacyFallback, true);
    assert.equal(Transport.buildDownloadPath(legacy, 42), '/__down?bytes=42');
    assert.equal(Transport.buildUploadPath(legacy, 42), '/__up');
    assert.deepEqual(Transport.buildLatencyRequest(legacy, { seq: 2 }), {
        method: 'GET',
        path: '/__down?seq=2&bytes=0'
    });
    assert.throws(() => Transport.negotiate(null, { downloadPayload: 'zero' }), /does not advertise measurementCapabilities/);
}

{
    const fallback = Transport.negotiate(capabilities({
        httpPingPath: '',
        httpPingMethods: []
    }), {});
    assert.equal(fallback.latencyUsesDownloadEndpoint, true);
    const request = new URL(Transport.buildLatencyRequest(fallback, { seq: 3 }).path, 'https://speed.example.test');
    assert.equal(request.pathname, '/measure/down');
    assert.equal(request.searchParams.get('n'), '0');
    assert.equal(request.searchParams.get('kind'), 'random');
}

{
    for (const unsafe of [
        'https://evil.example/down', '//evil.example/down', '/down?x=1', '/down#x',
        '/a/../down', '/a/%2e%2e/down', '/a/%5c/down', '/a//down', '/down/',
        ' /down', '/down '
    ]) {
        assert.throws(() => Transport.validateCapabilities(capabilities({ downloadPath: unsafe })), /(unsafe|unclean) downloadPath/);
    }
    assert.throws(() => Transport.validateCapabilities(capabilities({
        downloadPayloadParameter: 'n'
    })), /must not use the same query parameter/);
    assert.throws(() => Transport.validateCapabilities(capabilities({
        downloadBytesParameter: '9bytes'
    })), /unsafe downloadBytesParameter/);
    assert.throws(() => Transport.validateCapabilities(capabilities({
        uploadContentEncodings: ['gzip']
    })), /identity upload/);
    assert.throws(() => Transport.validateCapabilities(capabilities({
        responseCacheControl: 'no-store'
    })), /no-store, no-transform/);
    assert.throws(() => Transport.validateCapabilities(capabilities({
        httpPingMethods: ['POST']
    })), /supported GET or HEAD/);
}

{
    assert.deepEqual(Transport.preferencesFromConfig({
        measurementTransport: {
            downloadPayload: 'zero',
            downloadFraming: 'chunked',
            downloadChunkBytes: '8192',
            downloadFlush: true
        }
    }), {
        downloadPayload: 'zero',
        downloadFraming: 'chunked',
        downloadChunkBytes: 8192,
        downloadFlush: 'true',
        explicit: true
    });
    assert.equal(Transport.preferencesFromConfig({ downloadPayload: 'random' }).downloadPayload, 'random');
    assert.throws(() => Transport.normalizePreferences({ downloadFraming: 'magic' }), /auto, fixed, or chunked/);
}

{
    const headers = Transport.measurementRequestHeaders({ Authorization: 'Bearer test' }, true);
    assert.deepEqual(headers, {
        Authorization: 'Bearer test',
        'Cache-Control': 'no-store, no-transform',
        Pragma: 'no-cache',
        'Content-Type': 'application/octet-stream',
        'Content-Encoding': 'identity'
    });
    assert.equal(headers['Accept-Encoding'], undefined, 'Accept-Encoding is browser-managed and must not be forged');
}

{
    const selection = strictSelection();
    const good = response(new Uint8Array(0), {
        'Content-Type': 'application/octet-stream',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'zero',
        'X-Netspeed-Framing': 'chunked',
        'X-Netspeed-Chunk-Bytes': '4096',
        'X-Netspeed-Flush': 'false'
    });
    const evidence = Transport.verifyDownloadResponse(good, selection, 8192);
    assert.equal(evidence.payload, 'zero');
    assert.equal(evidence.proxyBuffering, 'no');

    assert.throws(() => Transport.verifyDownloadResponse(response(new Uint8Array(0), {
        'Content-Encoding': 'gzip',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'zero',
        'X-Netspeed-Framing': 'chunked',
        'X-Netspeed-Chunk-Bytes': '4096',
        'X-Netspeed-Flush': 'false'
    }), selection, 8192), /unsupported Content-Encoding/);

    assert.throws(() => Transport.verifyDownloadResponse(response(new Uint8Array(0), {
        'Cache-Control': 'no-store',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'zero',
        'X-Netspeed-Framing': 'chunked',
        'X-Netspeed-Chunk-Bytes': '4096',
        'X-Netspeed-Flush': 'false'
    }), selection, 8192), /no-store, no-transform/);

    assert.throws(() => Transport.verifyDownloadResponse(response(new Uint8Array(0), {
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'zero',
        'X-Netspeed-Framing': 'chunked',
        'X-Netspeed-Chunk-Bytes': '4096',
        'X-Netspeed-Flush': 'true'
    }), selection, 8192), /response flush/);
}

{
    const fixed = Transport.negotiate(capabilities(), {
        downloadPayload: 'random', downloadFraming: 'fixed', downloadChunkBytes: 65536, downloadFlush: false
    });
    const good = response(new Uint8Array(0), {
        'Content-Length': '12',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'random',
        'X-Netspeed-Framing': 'fixed',
        'X-Netspeed-Chunk-Bytes': '65536',
        'X-Netspeed-Flush': 'false'
    });
    Transport.verifyDownloadResponse(good, fixed, 12);
    assert.throws(() => Transport.verifyDownloadResponse(response(new Uint8Array(0), {
        'Content-Length': '1.2e1',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'random',
        'X-Netspeed-Framing': 'fixed',
        'X-Netspeed-Chunk-Bytes': '65536',
        'X-Netspeed-Flush': 'false'
    }), fixed, 12), /invalid or missing Content-Length/);

    assert.throws(() => Transport.verifyDownloadResponse(response(new Uint8Array(0), {
        'Content-Length': '11',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'download',
        'X-Netspeed-Payload': 'random',
        'X-Netspeed-Framing': 'fixed',
        'X-Netspeed-Chunk-Bytes': '65536',
        'X-Netspeed-Flush': 'false'
    }), fixed, 12), /Content-Length/);
}

{
    const selection = strictSelection();
    const upload = response(JSON.stringify({ ok: true }), {
        'Content-Type': 'application/json',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'upload',
        'X-Netspeed-Payload': 'discarded',
        'X-Netspeed-Framing': 'chunked',
        'X-Netspeed-Content-Encoding': 'identity',
        'X-Netspeed-Expected-Bytes': '8',
        'X-Netspeed-Accepted-Bytes': '8',
        'X-Netspeed-Upload-Duration-Ns': '2000000'
    });
    const evidence = Transport.verifyUploadResponse(upload, selection, 8, 'chunked');
    assert.equal(evidence.durationNs, 2000000);

    const latency = response(null, {
        'Content-Length': '0',
        'Cache-Control': 'no-store, no-transform',
        'X-Accel-Buffering': 'no',
        'X-Netspeed-Measurement': 'latency'
    });
    Transport.verifyDedicatedLatencyResponse(latency, selection);
}

console.log('browser HTTP transport negotiation tests passed');
