/**
 * Browser-side HTTP measurement transport negotiation and verification.
 *
 * This module mirrors the versioned daemon contract used by the native Go and
 * C clients. It is intentionally dependency-free and is exposed as both a
 * browser global and a CommonJS module for the repository's Node tests.
 */
const NetspeedHTTPTransport = (function() {
    'use strict';

    const TRANSPORT_VERSION = 1;
    const DEFAULT_CHUNK_BYTES = 1 << 20;
    const CACHE_CONTROL = 'no-store, no-transform';
    const PREFERENCE_AUTO = 'auto';
    const WEBSOCKET_PING_PROTOCOL = 'netspeed.ping.v1';
    const WEBSOCKET_PING_PAYLOAD_BYTES = 16;
    const PARAMETER_NAME = /^[A-Za-z_.-][A-Za-z0-9_.-]*$/;
    const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

    function normalizeToken(value) {
        return String(value ?? '').trim().toLowerCase();
    }

    function containsToken(values, target) {
        const wanted = normalizeToken(target);
        return Array.isArray(values) && values.some(value => normalizeToken(value) === wanted);
    }

    function hasDirective(value, target) {
        const wanted = normalizeToken(target);
        return String(value ?? '').split(',').some(part => normalizeToken(part.split(';', 1)[0]) === wanted);
    }

    function parseHeaderRequirement(raw) {
        const value = String(raw ?? '').trim();
        if (!value) return null;
        if (/\r|\n/.test(value)) throw new Error('proxyBufferSuppressionHeader contains a line break');
        const separator = value.indexOf(':');
        if (separator <= 0) throw new Error(`invalid proxyBufferSuppressionHeader ${JSON.stringify(value)}`);
        const name = value.slice(0, separator).trim();
        const expectedValue = value.slice(separator + 1).trim();
        if (!HEADER_NAME.test(name) || !expectedValue) {
            throw new Error(`invalid proxyBufferSuppressionHeader ${JSON.stringify(value)}`);
        }
        return { name, value: expectedValue };
    }

    function validateEndpointPath(name, raw, required) {
        if (typeof raw !== 'string') {
            if (!required && (raw === undefined || raw === null)) return '';
            throw new Error(`${name} must be a string`);
        }
        const value = raw;
        if (!value) {
            if (required) throw new Error(`${name} is required`);
            return '';
        }
        if (value.trim() !== value || !value.startsWith('/') || value.startsWith('//') || value.includes('\\') ||
            value.includes('?') || value.includes('#')) {
            throw new Error(`unsafe ${name} ${JSON.stringify(value)}`);
        }

        let decoded;
        try {
            decoded = decodeURIComponent(value);
        } catch (_) {
            throw new Error(`unsafe ${name} ${JSON.stringify(value)}`);
        }
        if (!decoded.startsWith('/') || decoded.startsWith('//') || decoded.includes('\\') || decoded.includes('?') || decoded.includes('#')) {
            throw new Error(`unsafe ${name} ${JSON.stringify(value)}`);
        }
        const segments = decoded.split('/');
        if (segments.some(segment => segment === '.' || segment === '..') ||
            decoded.includes('//') || (decoded.length > 1 && decoded.endsWith('/'))) {
            throw new Error(`unclean ${name} ${JSON.stringify(value)}`);
        }

        const parsed = new URL(value, 'https://netspeed.invalid/');
        if (parsed.origin !== 'https://netspeed.invalid' || parsed.username || parsed.password ||
            parsed.search || parsed.hash || parsed.pathname !== value) {
            throw new Error(`unsafe ${name} ${JSON.stringify(value)}`);
        }
        return value;
    }

    function validateParameterName(name, raw) {
        if (typeof raw !== 'string' || !PARAMETER_NAME.test(raw)) {
            throw new Error(`unsafe ${name} ${JSON.stringify(raw)}`);
        }
        return raw;
    }

    function requireInteger(name, raw, minimum = Number.MIN_SAFE_INTEGER) {
        if (!Number.isSafeInteger(raw) || raw < minimum) {
            throw new Error(`${name} must be an integer${minimum > Number.MIN_SAFE_INTEGER ? ` at least ${minimum}` : ''}`);
        }
        return raw;
    }

    function validateCapabilities(raw) {
        if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
            throw new Error('measurement capabilities are missing');
        }
        const version = requireInteger('measurementCapabilities.version', raw.version, 1);
        if (version < TRANSPORT_VERSION) {
            throw new Error(`server HTTP transport capability version ${version} is too old; need ${TRANSPORT_VERSION}`);
        }

        const downloadPath = validateEndpointPath('downloadPath', raw.downloadPath, true);
        const uploadPath = validateEndpointPath('uploadPath', raw.uploadPath, true);
        const httpPingPath = validateEndpointPath('httpPingPath', raw.httpPingPath ?? '', false);
        const webSocketPingPath = validateEndpointPath('webSocketPingPath', raw.webSocketPingPath ?? '', false);
        const webSocketPingProtocol = String(raw.webSocketPingProtocol ?? '').trim();
        const webSocketPingPayloadBytes = raw.webSocketPingPayloadBytes === undefined || raw.webSocketPingPayloadBytes === null
            ? 0
            : requireInteger('webSocketPingPayloadBytes', raw.webSocketPingPayloadBytes, 0);
        if (!webSocketPingPath) {
            if (webSocketPingProtocol || webSocketPingPayloadBytes !== 0) {
                throw new Error('WebSocket ping protocol metadata is advertised without webSocketPingPath');
            }
        } else {
            if (webSocketPingProtocol !== WEBSOCKET_PING_PROTOCOL) {
                throw new Error(`unsupported WebSocket ping protocol ${JSON.stringify(webSocketPingProtocol)}`);
            }
            if (webSocketPingPayloadBytes !== WEBSOCKET_PING_PAYLOAD_BYTES) {
                throw new Error(`unsupported WebSocket ping payload size ${webSocketPingPayloadBytes}; need ${WEBSOCKET_PING_PAYLOAD_BYTES}`);
            }
        }

        const parameterFields = [
            'downloadBytesParameter',
            'downloadPayloadParameter',
            'downloadFramingParameter',
            'downloadChunkBytesParameter',
            'downloadFlushParameter'
        ];
        const parameters = {};
        const seen = new Map();
        for (const field of parameterFields) {
            const value = validateParameterName(field, raw[field]);
            if (seen.has(value)) {
                throw new Error(`${seen.get(value)} and ${field} must not use the same query parameter ${JSON.stringify(value)}`);
            }
            seen.set(value, field);
            parameters[field] = value;
        }
        const uploadBytesParameter = validateParameterName('uploadBytesParameter', raw.uploadBytesParameter);

        const downloadPayloads = Array.isArray(raw.downloadPayloads)
            ? raw.downloadPayloads.map(normalizeToken).filter(Boolean)
            : [];
        const defaultDownloadPayload = normalizeToken(raw.defaultDownloadPayload);
        if (defaultDownloadPayload !== 'random' && defaultDownloadPayload !== 'zero') {
            throw new Error(`invalid default download payload ${JSON.stringify(raw.defaultDownloadPayload)}`);
        }
        if (!containsToken(downloadPayloads, defaultDownloadPayload)) {
            throw new Error(`default download payload ${JSON.stringify(defaultDownloadPayload)} is not advertised as supported`);
        }
        if (!containsToken(downloadPayloads, 'random')) {
            throw new Error(`transport version ${TRANSPORT_VERSION} must support pseudorandom downloads`);
        }

        const downloadFramings = Array.isArray(raw.downloadFramings)
            ? raw.downloadFramings.map(normalizeToken).filter(Boolean)
            : [];
        const defaultDownloadFraming = normalizeToken(raw.defaultDownloadFraming);
        if (defaultDownloadFraming !== 'fixed' && defaultDownloadFraming !== 'chunked') {
            throw new Error(`invalid default download framing ${JSON.stringify(raw.defaultDownloadFraming)}`);
        }
        if (!containsToken(downloadFramings, defaultDownloadFraming)) {
            throw new Error(`default download framing ${JSON.stringify(defaultDownloadFraming)} is not advertised as supported`);
        }
        if (!containsToken(downloadFramings, 'fixed')) {
            throw new Error(`transport version ${TRANSPORT_VERSION} must support fixed framing`);
        }

        const minimumChunkBytes = requireInteger('minimumChunkBytes', raw.minimumChunkBytes, 1);
        const maximumChunkBytes = requireInteger('maximumChunkBytes', raw.maximumChunkBytes, minimumChunkBytes);
        const defaultChunkBytes = requireInteger('defaultChunkBytes', raw.defaultChunkBytes, minimumChunkBytes);
        if (defaultChunkBytes > maximumChunkBytes) {
            throw new Error(`default download chunk size ${defaultChunkBytes} is outside advertised range ${minimumChunkBytes}..${maximumChunkBytes}`);
        }

        const uploadContentEncodings = Array.isArray(raw.uploadContentEncodings)
            ? raw.uploadContentEncodings.map(normalizeToken).filter(Boolean)
            : [];
        if (!containsToken(uploadContentEncodings, 'identity')) {
            throw new Error('server does not advertise identity upload content encoding');
        }
        const responseCacheControl = String(raw.responseCacheControl ?? '').trim();
        if (raw.noTransform !== true || !hasDirective(responseCacheControl, 'no-store') ||
            !hasDirective(responseCacheControl, 'no-transform')) {
            throw new Error('server transport capabilities do not guarantee no-store, no-transform responses');
        }

        const httpPingMethods = Array.isArray(raw.httpPingMethods)
            ? raw.httpPingMethods.map(value => String(value).trim().toUpperCase()).filter(Boolean)
            : [];
        let preferredPingMethod = '';
        if (httpPingMethods.includes('GET')) preferredPingMethod = 'GET';
        else if (httpPingMethods.includes('HEAD')) preferredPingMethod = 'HEAD';
        if (httpPingPath && !preferredPingMethod) {
            throw new Error('httpPingPath is advertised without a supported GET or HEAD method');
        }

        const proxyHeader = parseHeaderRequirement(raw.proxyBufferSuppressionHeader);
        return {
            version,
            downloadPath,
            ...parameters,
            uploadPath,
            uploadBytesParameter,
            httpPingPath,
            httpPingMethods,
            preferredPingMethod,
            webSocketPingPath,
            webSocketPingProtocol,
            webSocketPingPayloadBytes,
            warmConnectionPing: raw.warmConnectionPing === true,
            downloadPayloads,
            downloadFramings,
            defaultDownloadPayload,
            defaultDownloadFraming,
            defaultChunkBytes,
            minimumChunkBytes,
            maximumChunkBytes,
            uploadContentEncodings,
            responseCacheControl,
            noTransform: true,
            proxyBufferSuppressionHeader: proxyHeader ? `${proxyHeader.name}: ${proxyHeader.value}` : '',
            proxyBufferSuppressionRequirement: proxyHeader,
            proxyRequestBufferingAdvisory: raw.proxyRequestBufferingAdvisory === true
        };
    }

    function normalizePreferences(raw = {}) {
        const payload = normalizeToken(raw.downloadPayload) || PREFERENCE_AUTO;
        const framing = normalizeToken(raw.downloadFraming) || PREFERENCE_AUTO;
        let flush;
        if (raw.downloadFlush === true) flush = 'true';
        else if (raw.downloadFlush === false) flush = 'false';
        else flush = normalizeToken(raw.downloadFlush) || PREFERENCE_AUTO;

        if (!['auto', 'random', 'zero'].includes(payload)) {
            throw new Error('download payload must be auto, random, or zero');
        }
        if (!['auto', 'fixed', 'chunked'].includes(framing)) {
            throw new Error('download framing must be auto, fixed, or chunked');
        }
        if (!['auto', 'true', 'false'].includes(flush)) {
            throw new Error('download flush must be auto, true, or false');
        }

        let chunkBytes = raw.downloadChunkBytes;
        if (chunkBytes === undefined || chunkBytes === null || chunkBytes === '') chunkBytes = 0;
        if (typeof chunkBytes === 'string' && /^\d+$/.test(chunkBytes.trim())) chunkBytes = Number(chunkBytes.trim());
        if (!Number.isSafeInteger(chunkBytes) || chunkBytes < 0) {
            throw new Error('download chunk bytes cannot be negative or non-integral');
        }
        return {
            downloadPayload: payload,
            downloadFraming: framing,
            downloadChunkBytes: chunkBytes,
            downloadFlush: flush,
            explicit: payload !== PREFERENCE_AUTO || framing !== PREFERENCE_AUTO || chunkBytes !== 0 || flush !== PREFERENCE_AUTO
        };
    }

    function preferencesFromConfig(config) {
        const source = config && typeof config === 'object' ? config : {};
        const nested = source.measurementTransport && typeof source.measurementTransport === 'object'
            ? source.measurementTransport
            : {};
        const pick = name => source[name] !== undefined ? source[name] : nested[name];
        return normalizePreferences({
            downloadPayload: pick('downloadPayload'),
            downloadFraming: pick('downloadFraming'),
            downloadChunkBytes: pick('downloadChunkBytes'),
            downloadFlush: pick('downloadFlush')
        });
    }

    function legacySelection() {
        return {
            capabilityVersion: 0,
            legacyFallback: true,
            downloadPath: '/__down',
            downloadBytesParameter: 'bytes',
            downloadPayloadParameter: '',
            downloadFramingParameter: '',
            downloadChunkBytesParameter: '',
            downloadFlushParameter: '',
            downloadPayload: 'random',
            downloadFraming: 'fixed',
            downloadChunkBytes: DEFAULT_CHUNK_BYTES,
            downloadFlush: false,
            uploadPath: '/__up',
            uploadBytesParameter: '',
            uploadContentEncoding: 'identity',
            latencyPath: '/__down',
            latencyMethod: 'GET',
            latencyUsesDownloadEndpoint: true,
            warmConnectionPing: false,
            noTransform: false,
            responseCacheControl: '',
            proxyBufferSuppressionHeader: '',
            proxyRequestBufferingAdvisory: false,
            webSocketPingPath: '',
            webSocketPingProtocol: '',
            webSocketPingPayloadBytes: 0,
            preferredLatencyTransport: 'http',
            httpFallbackAvailable: true
        };
    }

    function negotiate(rawCapabilities, rawPreferences = {}) {
        const preferences = normalizePreferences(rawPreferences);
        if (rawCapabilities === undefined || rawCapabilities === null) {
            if (preferences.explicit) {
                throw new Error(`server does not advertise measurementCapabilities; explicit HTTP transport controls require transport version ${TRANSPORT_VERSION}`);
            }
            return legacySelection();
        }

        const capabilities = validateCapabilities(rawCapabilities);
        const payload = preferences.downloadPayload === PREFERENCE_AUTO
            ? capabilities.defaultDownloadPayload
            : preferences.downloadPayload;
        if (!containsToken(capabilities.downloadPayloads, payload)) {
            throw new Error(`server does not support download payload ${JSON.stringify(payload)}`);
        }
        const framing = preferences.downloadFraming === PREFERENCE_AUTO
            ? capabilities.defaultDownloadFraming
            : preferences.downloadFraming;
        if (!containsToken(capabilities.downloadFramings, framing)) {
            throw new Error(`server does not support download framing ${JSON.stringify(framing)}`);
        }
        const chunkBytes = preferences.downloadChunkBytes || capabilities.defaultChunkBytes;
        if (chunkBytes < capabilities.minimumChunkBytes || chunkBytes > capabilities.maximumChunkBytes) {
            throw new Error(`download chunk size ${chunkBytes} is outside server range ${capabilities.minimumChunkBytes}..${capabilities.maximumChunkBytes}`);
        }
        const flush = preferences.downloadFlush === PREFERENCE_AUTO
            ? framing === 'chunked'
            : preferences.downloadFlush === 'true';

        let latencyPath = capabilities.httpPingPath;
        let latencyMethod = capabilities.preferredPingMethod;
        let latencyUsesDownloadEndpoint = false;
        if (!latencyPath) {
            latencyPath = capabilities.downloadPath;
            latencyMethod = 'GET';
            latencyUsesDownloadEndpoint = true;
        }

        return {
            capabilityVersion: capabilities.version,
            legacyFallback: false,
            downloadPath: capabilities.downloadPath,
            downloadBytesParameter: capabilities.downloadBytesParameter,
            downloadPayloadParameter: capabilities.downloadPayloadParameter,
            downloadFramingParameter: capabilities.downloadFramingParameter,
            downloadChunkBytesParameter: capabilities.downloadChunkBytesParameter,
            downloadFlushParameter: capabilities.downloadFlushParameter,
            downloadPayload: payload,
            downloadFraming: framing,
            downloadChunkBytes: chunkBytes,
            downloadFlush: flush,
            uploadPath: capabilities.uploadPath,
            uploadBytesParameter: capabilities.uploadBytesParameter,
            uploadContentEncoding: 'identity',
            latencyPath,
            latencyMethod,
            latencyUsesDownloadEndpoint,
            warmConnectionPing: capabilities.warmConnectionPing,
            noTransform: capabilities.noTransform,
            responseCacheControl: capabilities.responseCacheControl,
            proxyBufferSuppressionHeader: capabilities.proxyBufferSuppressionHeader,
            proxyRequestBufferingAdvisory: capabilities.proxyRequestBufferingAdvisory,
            webSocketPingPath: capabilities.webSocketPingPath,
            webSocketPingProtocol: capabilities.webSocketPingProtocol,
            webSocketPingPayloadBytes: capabilities.webSocketPingPayloadBytes,
            preferredLatencyTransport: capabilities.webSocketPingPath ? 'websocket' : 'http',
            httpFallbackAvailable: true
        };
    }

    function addLabels(params, labels) {
        if (!labels || typeof labels !== 'object') return;
        for (const [name, value] of Object.entries(labels)) {
            if (value === undefined || value === null || value === '') continue;
            params.set(name, String(value));
        }
    }

    function appendQuery(path, params) {
        const query = params.toString();
        return query ? `${path}?${query}` : path;
    }

    function buildDownloadPath(selection, bytes, labels = {}) {
        if (!Number.isSafeInteger(bytes) || bytes < 0) throw new Error(`invalid download byte count ${bytes}`);
        const params = new URLSearchParams();
        addLabels(params, labels);
        params.set(selection.downloadBytesParameter || 'bytes', String(bytes));
        if (!selection.legacyFallback) {
            params.set(selection.downloadPayloadParameter, selection.downloadPayload);
            params.set(selection.downloadFramingParameter, selection.downloadFraming);
            params.set(selection.downloadChunkBytesParameter, String(selection.downloadChunkBytes));
            params.set(selection.downloadFlushParameter, String(selection.downloadFlush));
        }
        return appendQuery(selection.downloadPath, params);
    }

    function buildUploadPath(selection, bytes, labels = {}) {
        if (!Number.isSafeInteger(bytes) || bytes < 0) throw new Error(`invalid upload byte count ${bytes}`);
        const params = new URLSearchParams();
        addLabels(params, labels);
        if (selection.uploadBytesParameter) params.set(selection.uploadBytesParameter, String(bytes));
        return appendQuery(selection.uploadPath, params);
    }

    function buildLatencyRequest(selection, labels = {}) {
        if (selection.latencyUsesDownloadEndpoint) {
            return {
                method: selection.latencyMethod || 'GET',
                path: buildDownloadPath(selection, 0, labels)
            };
        }
        const params = new URLSearchParams();
        addLabels(params, labels);
        return {
            method: selection.latencyMethod || 'GET',
            path: appendQuery(selection.latencyPath, params)
        };
    }

    function measurementRequestHeaders(base = {}, upload = false) {
        const headers = {
            ...base,
            'Cache-Control': CACHE_CONTROL,
            'Pragma': 'no-cache'
        };
        if (upload) {
            headers['Content-Type'] = 'application/octet-stream';
            headers['Content-Encoding'] = 'identity';
        }
        return headers;
    }

    function responseHeader(response, name) {
        return response && response.headers && typeof response.headers.get === 'function'
            ? response.headers.get(name)
            : null;
    }

    function verifyIdentityContentEncoding(response) {
        const raw = responseHeader(response, 'Content-Encoding');
        if (raw === null || String(raw).trim() === '') return 'absent';
        for (const part of String(raw).split(',')) {
            const encoding = normalizeToken(part);
            if (!encoding || encoding !== 'identity') {
                throw new Error(`measurement response used unsupported Content-Encoding ${JSON.stringify(raw)}`);
            }
        }
        return 'identity';
    }

    function verifyCommonResponse(response, selection, expectedMeasurement) {
        const contentEncoding = verifyIdentityContentEncoding(response);
        const evidence = {
            contentEncoding,
            cacheControl: responseHeader(response, 'Cache-Control') || '',
            measurement: responseHeader(response, 'X-Netspeed-Measurement') || '',
            proxyBuffering: null
        };
        if (selection.legacyFallback) return evidence;

        if (!hasDirective(evidence.cacheControl, 'no-store') || !hasDirective(evidence.cacheControl, 'no-transform')) {
            throw new Error(`measurement response Cache-Control ${JSON.stringify(evidence.cacheControl)} does not preserve no-store, no-transform`);
        }
        if (normalizeToken(evidence.measurement) !== normalizeToken(expectedMeasurement)) {
            throw new Error(`measurement response type ${JSON.stringify(evidence.measurement)}; expected ${JSON.stringify(expectedMeasurement)}`);
        }
        const proxyRequirement = parseHeaderRequirement(selection.proxyBufferSuppressionHeader);
        if (proxyRequirement) {
            const actual = responseHeader(response, proxyRequirement.name) || '';
            evidence.proxyBuffering = actual;
            if (normalizeToken(actual) !== normalizeToken(proxyRequirement.value)) {
                throw new Error(`measurement response ${proxyRequirement.name} ${JSON.stringify(actual)}; expected ${JSON.stringify(proxyRequirement.value)}`);
            }
        }
        return evidence;
    }

    function parseRequiredNonnegativeHeader(response, name) {
        const raw = responseHeader(response, name);
        if (raw === null || !/^\d+$/.test(String(raw).trim())) {
            throw new Error(`measurement response has invalid or missing ${name} ${JSON.stringify(raw)}`);
        }
        const value = Number(String(raw).trim());
        if (!Number.isSafeInteger(value) || value < 0) {
            throw new Error(`measurement response has invalid ${name} ${JSON.stringify(raw)}`);
        }
        return value;
    }

    function verifyDownloadResponse(response, selection, expectedBytes, expectedMeasurement = 'download') {
        const evidence = verifyCommonResponse(response, selection, expectedMeasurement);
        if (selection.legacyFallback) return evidence;

        const payload = normalizeToken(responseHeader(response, 'X-Netspeed-Payload'));
        const framing = normalizeToken(responseHeader(response, 'X-Netspeed-Framing'));
        const chunkBytes = parseRequiredNonnegativeHeader(response, 'X-Netspeed-Chunk-Bytes');
        const flush = normalizeToken(responseHeader(response, 'X-Netspeed-Flush'));
        if (payload !== selection.downloadPayload) {
            throw new Error(`download response payload ${JSON.stringify(payload)}; expected ${JSON.stringify(selection.downloadPayload)}`);
        }
        if (framing !== selection.downloadFraming) {
            throw new Error(`download response framing ${JSON.stringify(framing)}; expected ${JSON.stringify(selection.downloadFraming)}`);
        }
        if (chunkBytes !== selection.downloadChunkBytes) {
            throw new Error(`download response chunk size ${chunkBytes}; expected ${selection.downloadChunkBytes}`);
        }
        if (flush !== String(selection.downloadFlush)) {
            throw new Error(`download response flush ${JSON.stringify(flush)}; expected ${JSON.stringify(String(selection.downloadFlush))}`);
        }

        const contentLengthRaw = responseHeader(response, 'Content-Length');
        if (selection.downloadFraming === 'fixed') {
            const contentLength = parseRequiredNonnegativeHeader(response, 'Content-Length');
            if (contentLength !== expectedBytes) {
                throw new Error(`fixed download Content-Length ${contentLength}; expected ${expectedBytes}`);
            }
        } else if (selection.downloadFraming === 'chunked') {
            if (contentLengthRaw !== null) {
                throw new Error(`streamed download unexpectedly supplied Content-Length ${JSON.stringify(contentLengthRaw)}`);
            }
        } else {
            throw new Error(`unsupported negotiated download framing ${JSON.stringify(selection.downloadFraming)}`);
        }
        return { ...evidence, payload, framing, chunkBytes, flush: flush === 'true' };
    }

    function verifyDedicatedLatencyResponse(response, selection) {
        const evidence = verifyCommonResponse(response, selection, 'latency');
        const contentLengthRaw = responseHeader(response, 'Content-Length');
        if (contentLengthRaw !== null) {
            const contentLength = parseRequiredNonnegativeHeader(response, 'Content-Length');
            if (contentLength !== 0) {
                throw new Error(`latency response Content-Length ${contentLength}; expected 0`);
            }
        }
        return evidence;
    }

    function verifyUploadResponse(response, selection, expectedBytes, expectedFraming) {
        const evidence = verifyCommonResponse(response, selection, 'upload');
        if (selection.legacyFallback) return evidence;

        const expectedHeaders = {
            'X-Netspeed-Payload': 'discarded',
            'X-Netspeed-Framing': expectedFraming,
            'X-Netspeed-Content-Encoding': 'identity'
        };
        for (const [name, expected] of Object.entries(expectedHeaders)) {
            const actual = normalizeToken(responseHeader(response, name));
            if (actual !== normalizeToken(expected)) {
                throw new Error(`upload response ${name} ${JSON.stringify(actual)}; expected ${JSON.stringify(expected)}`);
            }
        }
        if (selection.uploadBytesParameter) {
            const declared = parseRequiredNonnegativeHeader(response, 'X-Netspeed-Expected-Bytes');
            if (declared !== expectedBytes) {
                throw new Error(`upload response expected-byte count ${declared}; expected ${expectedBytes}`);
            }
        }
        const accepted = parseRequiredNonnegativeHeader(response, 'X-Netspeed-Accepted-Bytes');
        if (accepted !== expectedBytes) {
            throw new Error(`upload response accepted-byte count ${accepted}; expected ${expectedBytes}`);
        }
        const durationNs = parseRequiredNonnegativeHeader(response, 'X-Netspeed-Upload-Duration-Ns');
        if (durationNs <= 0) throw new Error(`upload response duration ${durationNs} is not positive`);
        return { ...evidence, acceptedBytes: accepted, durationNs, framing: expectedFraming };
    }

    function cloneSelection(selection) {
        return JSON.parse(JSON.stringify(selection));
    }

    return {
        TRANSPORT_VERSION,
        CACHE_CONTROL,
        WEBSOCKET_PING_PROTOCOL,
        WEBSOCKET_PING_PAYLOAD_BYTES,
        validateCapabilities,
        normalizePreferences,
        preferencesFromConfig,
        legacySelection,
        negotiate,
        buildDownloadPath,
        buildUploadPath,
        buildLatencyRequest,
        measurementRequestHeaders,
        verifyCommonResponse,
        verifyDownloadResponse,
        verifyDedicatedLatencyResponse,
        verifyUploadResponse,
        hasDirective,
        cloneSelection
    };
})();

if (typeof globalThis !== 'undefined') globalThis.NetspeedHTTPTransport = NetspeedHTTPTransport;
if (typeof module !== 'undefined' && module.exports) module.exports = NetspeedHTTPTransport;
