#!/usr/bin/env node

import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const webRoot = path.join(root, 'web');
const defaultOutputDirectory = path.join(webRoot, 'screenshots');
const fixedTime = Date.UTC(2026, 7, 29, 3, 41, 32);

const capabilities = {
  endpoints: {
    download: '/__down',
    upload: '/__up',
    latency: { http: { path: '/__ping', method: 'GET', warmConnection: true } }
  },
  download: {
    bytesParameter: 'bytes',
    payloadParameter: 'payload',
    framingParameter: 'framing',
    chunkBytesParameter: 'chunkBytes',
    flushParameter: 'flush',
    payloads: ['random', 'zero'],
    defaultPayload: 'random',
    framing: ['fixed', 'chunked'],
    defaultFraming: 'chunked',
    chunkBytes: { min: 4096, max: 1048576, default: 65536 },
    perChunkFlush: true
  },
  upload: {
    bytesParameter: 'bytes',
    contentEncodings: ['identity']
  },
  controls: {
    cacheControl: ['no-store', 'no-transform'],
    proxyBuffering: { responseHeader: 'X-Accel-Buffering', responseValue: 'no' }
  }
};

const metadata = {
  measurementProtocolVersion: 2,
  maxTransferBytes: 1073741824,
  maxConcurrentTransfersPerClient: 16,
  uploadReceiptVersion: 1,
  packetFrameVersion: 2,
  serverName: 'Cascade Edge 01',
  colo: 'RDM',
  clientIp: '198.51.100.24',
  asn: 64512,
  asOrganization: 'High Desert Fiber',
  city: 'Bend',
  region: 'Oregon',
  country: 'US',
  timezone: 'America/Los_Angeles',
  latitude: 44.0582,
  longitude: -121.3153,
  measurementCapabilities: capabilities
};

const locations = [
  { iata: 'RDM', city: 'Redmond', region: 'Oregon', cca2: 'US', lat: 44.2726, lon: -121.1739 }
];

const captures = [
  { html: 'index.html', file: 'standard.png', width: 1600, height: 1000, clipHeight: 850 },
  { html: 'alternate.html', file: 'observatory.png', width: 1600, height: 1000, clipHeight: 1000 },
  { html: 'phosphor.html', file: 'phosphor.png', width: 1600, height: 1000, clipHeight: 1000 }
];

const mimeTypes = new Map([
  ['.css', 'text/css; charset=utf-8'],
  ['.html', 'text/html; charset=utf-8'],
  ['.js', 'text/javascript; charset=utf-8'],
  ['.json', 'application/json; charset=utf-8'],
  ['.png', 'image/png'],
  ['.svg', 'image/svg+xml']
]);

function parseArguments(argv) {
  const options = {
    browser: process.env.NETSPEED_BROWSER || process.env.CHROME_PATH || '',
    outputDirectory: defaultOutputDirectory,
    selected: new Set()
  };

  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--browser') {
      options.browser = argv[++index] || '';
    } else if (argument === '--output-dir') {
      options.outputDirectory = path.resolve(argv[++index] || '');
    } else if (argument === '--page') {
      const name = argv[++index] || '';
      if (!['standard', 'observatory', 'phosphor'].includes(name)) {
        throw new Error(`unknown screenshot page: ${name}`);
      }
      options.selected.add(name);
    } else if (argument === '--help' || argument === '-h') {
      console.log('Usage: node scripts/capture_interfaces.mjs [--browser PATH] [--output-dir DIR] [--page standard|observatory|phosphor]');
      process.exit(0);
    } else {
      throw new Error(`unknown argument: ${argument}`);
    }
  }

  return options;
}

function findBrowser(explicitPath) {
  const candidates = [
    explicitPath,
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/Applications/Chromium.app/Contents/MacOS/Chromium'
  ].filter(Boolean);

  for (const candidate of candidates) {
    try {
      if (fs.statSync(candidate).isFile()) return candidate;
    } catch (_) {
      // Try the next conventional browser path.
    }
  }

  throw new Error('Chromium or Chrome was not found; set NETSPEED_BROWSER or pass --browser PATH');
}

function createServer() {
  const server = http.createServer((request, response) => {
    const requestURL = new URL(request.url || '/', 'http://127.0.0.1');
    if (requestURL.pathname === '/meta') {
      response.setHeader('Content-Type', mimeTypes.get('.json'));
      response.setHeader('Cache-Control', 'no-store');
      response.end(JSON.stringify(metadata));
      return;
    }
    if (requestURL.pathname === '/locations') {
      response.setHeader('Content-Type', mimeTypes.get('.json'));
      response.setHeader('Cache-Control', 'no-store');
      response.end(JSON.stringify(locations));
      return;
    }

    let relativePath;
    try {
      relativePath = decodeURIComponent(requestURL.pathname).replace(/^\/+/, '') || 'index.html';
    } catch (_) {
      response.statusCode = 400;
      response.end('bad path');
      return;
    }

    const resolvedPath = path.resolve(webRoot, relativePath);
    if (resolvedPath !== webRoot && !resolvedPath.startsWith(`${webRoot}${path.sep}`)) {
      response.statusCode = 403;
      response.end('forbidden');
      return;
    }

    let stat;
    try {
      stat = fs.statSync(resolvedPath);
    } catch (_) {
      response.statusCode = 404;
      response.end('not found');
      return;
    }
    if (!stat.isFile()) {
      response.statusCode = 404;
      response.end('not found');
      return;
    }

    response.setHeader('Content-Type', mimeTypes.get(path.extname(resolvedPath)) || 'application/octet-stream');
    response.setHeader('Cache-Control', 'no-store');
    fs.createReadStream(resolvedPath).pipe(response);
  });

  return server;
}

class DevToolsConnection {
  constructor(url) {
    if (typeof WebSocket !== 'function') {
      throw new Error('this capture tool requires a Node.js release with the built-in WebSocket API');
    }
    this.socket = new WebSocket(url);
    this.nextID = 0;
    this.pending = new Map();
    this.waiters = new Map();
  }

  async open() {
    await new Promise((resolve, reject) => {
      this.socket.addEventListener('open', resolve, { once: true });
      this.socket.addEventListener('error', reject, { once: true });
    });
    this.socket.addEventListener('message', event => this.handleMessage(event));
    this.socket.addEventListener('close', () => {
      for (const { reject } of this.pending.values()) reject(new Error('DevTools connection closed'));
      this.pending.clear();
    });
  }

  handleMessage(event) {
    const message = JSON.parse(event.data);
    if (message.id) {
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error(`${pending.method}: ${message.error.message}`));
      else pending.resolve(message.result);
      return;
    }

    const waiters = this.waiters.get(message.method);
    if (!waiters || waiters.length === 0) return;
    this.waiters.delete(message.method);
    for (const waiter of waiters) {
      clearTimeout(waiter.timer);
      waiter.resolve(message.params);
    }
  }

  send(method, params = {}) {
    const id = ++this.nextID;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { method, resolve, reject });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }

  waitFor(method, timeoutMilliseconds = 15000) {
    return new Promise((resolve, reject) => {
      const waiters = this.waiters.get(method) || [];
      const waiter = {
        resolve,
        reject,
        timer: setTimeout(() => {
          const current = this.waiters.get(method) || [];
          this.waiters.set(method, current.filter(candidate => candidate !== waiter));
          reject(new Error(`timed out waiting for ${method}`));
        }, timeoutMilliseconds)
      };
      waiters.push(waiter);
      this.waiters.set(method, waiters);
    });
  }

  close() {
    if (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING) {
      this.socket.close();
    }
  }
}

async function launchBrowser(executablePath) {
  const profileDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'netspeed-screenshot-'));
  const browser = spawn(executablePath, [
    '--headless=new',
    '--no-sandbox',
    '--disable-dev-shm-usage',
    '--disable-gpu',
    '--disable-background-networking',
    '--disable-component-update',
    '--disable-default-apps',
    '--disable-extensions',
    '--disable-sync',
    '--hide-scrollbars',
    '--metrics-recording-only',
    '--mute-audio',
    '--no-default-browser-check',
    '--no-first-run',
    '--remote-allow-origins=*',
    '--remote-debugging-port=0',
    `--user-data-dir=${profileDirectory}`,
    'about:blank'
  ], { stdio: ['ignore', 'ignore', 'pipe'] });

  let stderr = '';
  const browserWebSocketURL = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`Chromium startup timed out:\n${stderr}`)), 20000);
    browser.stderr.setEncoding('utf8');
    browser.stderr.on('data', chunk => {
      stderr += chunk;
      const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) {
        clearTimeout(timer);
        resolve(match[1]);
      }
    });
    browser.once('exit', code => {
      clearTimeout(timer);
      reject(new Error(`Chromium exited before DevTools was ready (status ${code}):\n${stderr}`));
    });
    browser.once('error', error => {
      clearTimeout(timer);
      reject(error);
    });
  });

  const debugPort = Number(new URL(browserWebSocketURL).port);
  const targets = await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json();
  const pageTarget = targets.find(target => target.type === 'page');
  if (!pageTarget) {
    browser.kill('SIGTERM');
    fs.rmSync(profileDirectory, { recursive: true, force: true });
    throw new Error('Chromium did not expose a page target');
  }

  const devtools = new DevToolsConnection(pageTarget.webSocketDebuggerUrl);
  await devtools.open();
  return { browser, devtools, profileDirectory, stderr: () => stderr };
}

function preloadSource() {
  return `(() => {
    'use strict';
    const fixed = ${fixedTime};
    const NativeDate = Date;
    class FixedDate extends NativeDate {
      constructor(...args) { super(...(args.length ? args : [fixed])); }
      static now() { return fixed; }
    }
    Object.setPrototypeOf(FixedDate, NativeDate);
    globalThis.Date = FixedDate;
    try { localStorage.setItem('theme', 'dark'); } catch (_) {}

    function renderMap(container, points = [], line = false) {
      if (!container) return;
      container.replaceChildren();
      const wrap = document.createElement('div');
      wrap.className = 'capture-map';
      wrap.innerHTML = '<div class="capture-map-grid"></div><div class="capture-map-label">CENTRAL OREGON</div>';
      container.appendChild(wrap);
      const coordinates = points.map(point => point.latlng || point);
      const minLat = Math.min(...coordinates.map(point => point[0]), 44.0);
      const maxLat = Math.max(...coordinates.map(point => point[0]), 44.3);
      const minLon = Math.min(...coordinates.map(point => point[1]), -121.4);
      const maxLon = Math.max(...coordinates.map(point => point[1]), -121.1);
      const project = ([lat, lon]) => ({
        x: 15 + 70 * ((lon - minLon) / (maxLon - minLon || 1)),
        y: 15 + 70 * (1 - (lat - minLat) / (maxLat - minLat || 1))
      });
      if (line && coordinates.length >= 2) {
        const start = project(coordinates[0]);
        const end = project(coordinates[1]);
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('viewBox', '0 0 100 100');
        svg.classList.add('capture-map-line');
        svg.innerHTML = '<line x1="' + start.x + '" y1="' + start.y + '" x2="' + end.x + '" y2="' + end.y + '" />';
        wrap.appendChild(svg);
      }
      points.forEach((point, index) => {
        const projected = project(point.latlng || point);
        const marker = document.createElement('div');
        marker.className = 'capture-map-marker ' + (index === 0 ? 'server' : 'client');
        marker.style.left = projected.x + '%';
        marker.style.top = projected.y + '%';
        marker.innerHTML = '<span></span><b>' + (point.label || (index === 0 ? 'RDM NODE' : 'BEND')) + '</b>';
        wrap.appendChild(marker);
      });
    }

    globalThis.L = {
      map(id) {
        const container = document.getElementById(id);
        const map = {
          container,
          points: [],
          setView(latlng) {
            map.center = latlng;
            renderMap(container, [{ latlng, label: 'RDM NODE' }]);
            return map;
          },
          fitBounds() {
            renderMap(container, map.points, true);
            return map;
          }
        };
        return map;
      },
      tileLayer() { return { addTo() { return this; } }; },
      divIcon(options) { return options; },
      marker(latlng) {
        return {
          label: '',
          bindTooltip(label) { this.label = label; return this; },
          addTo(map) {
            map.points.push({
              latlng,
              label: this.label.replace(/^Server: /, '') || (map.points.length ? 'BEND' : 'RDM NODE')
            });
            renderMap(map.container, map.points, map.points.length > 1);
            return this;
          }
        };
      },
      latLngBounds(points) { return points; },
      polyline(points) {
        return {
          addTo(map) {
            if (!map.points.length) {
              map.points = points.map((latlng, index) => ({ latlng, label: index ? 'RDM NODE' : 'BEND' }));
            }
            renderMap(map.container, map.points, true);
            return this;
          }
        };
      }
    };
  })();`;
}

const captureCSS = `
  * { animation: none !important; transition: none !important; caret-color: transparent !important; }
  html { scroll-behavior: auto !important; }
  html, body { scrollbar-width: none !important; }
  body::-webkit-scrollbar, *::-webkit-scrollbar { display: none !important; }
  .toast, .boxplot-tooltip { display: none !important; }
  .capture-map { position: relative; width: 100%; height: 100%; overflow: hidden; background: linear-gradient(135deg, #172438, #24364c); }
  .capture-map-grid { position: absolute; inset: 0; background-image: linear-gradient(rgba(255,255,255,.055) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,.055) 1px, transparent 1px); background-size: 32px 32px; }
  .capture-map-label { position: absolute; right: 12px; bottom: 10px; font: 10px monospace; letter-spacing: .14em; color: #93a4b8; }
  .capture-map-line { position: absolute; inset: 0; width: 100%; height: 100%; }
  .capture-map-line line { stroke: #8b5cf6; stroke-width: .75; stroke-dasharray: 2 2; }
  .capture-map-marker { position: absolute; transform: translate(-50%, -50%); font: 9px monospace; color: #e5edf7; white-space: nowrap; }
  .capture-map-marker span { display: block; width: 10px; height: 10px; border-radius: 50%; background: #3b82f6; box-shadow: 0 0 0 4px rgba(59,130,246,.18); margin: auto; }
  .capture-map-marker.client span { background: #10b981; box-shadow: 0 0 0 4px rgba(16,185,129,.18); }
  .capture-map-marker b { display: block; margin-top: 7px; padding: 3px 5px; background: rgba(15,23,42,.85); border-radius: 3px; font-weight: 500; }
`;

function fixtureSource() {
  return `(() => {
    'use strict';
    const engine = window.NetspeedApp.SpeedTest;
    let callbacks = {};
    engine.setCallbacks = value => { callbacks = { ...callbacks, ...value }; };
    engine.pause = () => {};
    engine.resume = () => {};
    engine.stop = () => {};
    engine.exportResults = () => JSON.stringify({ fixture: true }, null, 2);

    const meta = ${JSON.stringify(metadata)};
    const locations = ${JSON.stringify(locations)};
    const downloadSamples = [
      ['100kB', 453.8, 1762], ['100kB', 472.1, 1694], ['100kB', 481.2, 1662],
      ['1MB', 482.4, 16583], ['1MB', 486.7, 16437], ['1MB', 491.3, 16284]
    ].map(([profile, mbps, durationMs]) => ({
      direction: 'download', profile, mbps, durationMs, bytes: profile === '1MB' ? 1000000 : 100000
    }));
    const uploadSamples = [
      ['100kB', 84.8, 9434], ['100kB', 88.9, 8999], ['100kB', 91.7, 8724],
      ['1MB', 89.7, 89186], ['1MB', 92.4, 86580], ['1MB', 95.1, 84122]
    ].map(([profile, mbps, durationMs]) => ({
      direction: 'upload', profile, mbps, durationMs, bytes: profile === '1MB' ? 1000000 : 100000
    }));
    const unloaded = [11.8, 12.0, 12.1, 12.3, 12.4, 12.6, 12.7, 12.8, 13.0, 13.2]
      .map(rttMs => ({ condition: 'unloaded', rttMs, connectionReused: true }));
    const loadedDownload = [24.8, 26.1, 27.9, 29.4, 31.2]
      .map(rttMs => ({ condition: 'download', rttMs, loadOverlapped: true, connectionReused: true }));
    const loadedUpload = [31.5, 33.2, 34.8, 36.4, 38.0]
      .map(rttMs => ({ condition: 'upload', rttMs, loadOverlapped: true, connectionReused: true }));
    const packetLoss = {
      unavailable: false,
      sent: 1000,
      received: 998,
      acknowledgementsReceived: 998,
      forwardReceived: 999,
      transactionLossPercent: 0.2,
      forwardLossPercent: 0.1,
      reverseAcknowledgementLossPercent: 0.1,
      lossPercent: 0.2,
      rttStatsMs: { min: 11.9, median: 13.1, p90: 15.4 },
      jitterMs: 1.7
    };
    const lossPattern = {
      type: 'random',
      lossDistribution: [0, 0, 1, 0, 0, 0, 0, 1, 0, 0],
      burstCount: 2,
      maxBurstLength: 1,
      avgBurstLength: 1
    };
    const dataChannelStats = { connectionType: 'srflx', protocol: 'udp', currentRoundTripTime: 13.1 };
    const bandwidthEstimate = {
      downloadTrend: 'stable', uploadTrend: 'stable', downloadPeakMbps: 491.3, uploadPeakMbps: 95.1,
      downloadSustainedMbps: 486.7, uploadSustainedMbps: 92.4,
      downloadVariability: 0.031, uploadVariability: 0.047
    };
    const networkQualityScore = {
      overall: 96,
      grade: 'Excellent',
      description: 'Fast, responsive, and stable for demanding interactive use.',
      components: { bandwidth: 99, latency: 97, stability: 94, reliability: 98 }
    };
    const testConfidence = {
      overall: 'high',
      metrics: {
        sampleCount: { adequate: true, downloadWindows: 6, uploadWindows: 6, unloadedLatency: 10 },
        coefficientOfVariation: { acceptable: true, download: 3, upload: 5, latency: 4 },
        timingAccuracy: { accurate: true },
        loadedOverlap: { complete: true, downloadAccepted: 5, uploadAccepted: 5 },
        packetTest: { completed: true }
      },
      warnings: []
    };
    const summary = {
      downloadMbps: 486.7,
      uploadMbps: 92.4,
      latencyUnloadedMs: 12.6,
      latencyDownloadMs: 27.9,
      latencyUploadMs: 34.8,
      jitterMs: 2.1,
      packetLossPercent: 0.2
    };
    const quality = { videoStreaming: 'Great', gaming: 'Great', videoChatting: 'Great' };
    const results = {
      meta,
      locations,
      throughputSamples: [...downloadSamples, ...uploadSamples],
      latencySamples: [...unloaded, ...loadedDownload, ...loadedUpload],
      packetLoss,
      lossPattern,
      dataChannelStats,
      bandwidthEstimate,
      networkQualityScore,
      testConfidence,
      startTime: Date.now() - 12000,
      endTime: Date.now()
    };
    const stage = (name, outcome, detail = {}) => callbacks.onStageChange?.({ stage: name, outcome, ...detail });

    engine.start = async () => {
      for (const name of ['meta', 'latency', 'download', 'upload', 'loaded-latency', 'packet-loss', 'complete']) {
        stage(name, 'pending');
      }
      stage('meta', 'running');
      callbacks.onProgress?.('meta', 0);
      callbacks.onMetaReceived?.(meta, locations);
      stage('meta', 'succeeded');

      stage('latency', 'running');
      callbacks.onProgress?.('latency', 0);
      unloaded.forEach((sample, index) => callbacks.onLatencyProgress?.('unloaded', index + 1, unloaded.length, sample));
      stage('latency', 'succeeded');

      stage('download', 'running');
      callbacks.onProgress?.('download', 0);
      downloadSamples.forEach((sample, index) => callbacks.onDownloadProgress?.(
        sample.profile, (index % 3) + 1, 3, sample, index + 1, downloadSamples.length
      ));
      stage('download', 'succeeded');

      stage('upload', 'running');
      callbacks.onProgress?.('upload', 0);
      uploadSamples.forEach((sample, index) => callbacks.onUploadProgress?.(
        sample.profile, (index % 3) + 1, 3, sample, index + 1, uploadSamples.length
      ));
      stage('upload', 'succeeded');

      stage('loaded-latency', 'running');
      callbacks.onProgress?.('loaded-latency', 0);
      loadedDownload.forEach((sample, index) => callbacks.onLatencyProgress?.('download', index + 1, loadedDownload.length, sample));
      loadedUpload.forEach((sample, index) => callbacks.onLatencyProgress?.('upload', index + 1, loadedUpload.length, sample));
      stage('loaded-latency', 'succeeded');

      stage('packet-loss', 'running');
      callbacks.onProgress?.('packet-loss', 0);
      callbacks.onPacketLossProgress?.(1000, 1000, 998);
      stage('packet-loss', 'succeeded');

      stage('complete', 'running');
      callbacks.onComplete?.(results, summary, quality);
      stage('complete', 'succeeded');
      return { results, summary, quality };
    };

    return window.NetspeedApp.startTest();
  })()`;
}

function evaluationValue(result, context) {
  if (result.exceptionDetails) {
    const description = result.exceptionDetails.exception?.description || result.exceptionDetails.text || 'unknown browser exception';
    throw new Error(`${context}: ${description}`);
  }
  return result.result?.value;
}

async function evaluate(devtools, expression, context, awaitPromise = false) {
  const result = await devtools.send('Runtime.evaluate', {
    expression,
    awaitPromise,
    returnByValue: true,
    userGesture: false
  });
  return evaluationValue(result, context);
}

async function waitForApplication(devtools) {
  await evaluate(devtools, `new Promise((resolve, reject) => {
    let attempts = 0;
    (function check() {
      if (window.NetspeedApp?.SpeedTest) return resolve(true);
      if (++attempts > 300) return reject(new Error('NetspeedApp initialization timed out'));
      setTimeout(check, 25);
    })();
  })`, 'wait for NetspeedApp', true);
}

async function settlePage(devtools) {
  await evaluate(devtools, `new Promise(resolve => {
    const finish = () => requestAnimationFrame(() => requestAnimationFrame(resolve));
    if (document.fonts?.ready) document.fonts.ready.then(finish, finish);
    else finish();
  })`, 'settle rendered page', true);
}

function assertionSource(hasStageRail) {
  return `(() => {
    const text = selector => document.querySelector(selector)?.textContent.trim() || '';
    const expected = {
      '#downloadSpeed': '486.7',
      '#uploadSpeed': '92.4',
      '#latencyValue': '12.6',
      '#jitterValue': '2.1',
      '#packetLossValue': '0.20'
    };
    for (const [selector, value] of Object.entries(expected)) {
      if (text(selector) !== value) throw new Error(selector + ' expected ' + value + ', got ' + text(selector));
    }
    if (text('#progressStatus') !== 'Test complete') {
      throw new Error('progress status did not reach Test complete');
    }
    if (document.querySelectorAll('.test-card').length < 4) {
      throw new Error('throughput result cards were not rendered');
    }
    if (document.documentElement.dataset.measurementOutcome !== 'succeeded') {
      throw new Error('structured measurement outcome did not reach succeeded');
    }
    const clocks = [...document.querySelectorAll('[data-live-clock]')];
    for (const clock of clocks) {
      if (clock.textContent.trim() !== '20:41:32') throw new Error('live clock was not populated deterministically');
    }
    const stages = [...document.querySelectorAll('[data-progress-stage]')];
    if (${hasStageRail ? 'true' : 'false'}) {
      if (stages.length !== 7) throw new Error('expected seven structured progress stages');
      if (stages.some(stage => stage.dataset.outcome !== 'succeeded' || !stage.classList.contains('is-complete'))) {
        throw new Error('one or more progress stages did not render as complete');
      }
      if (text('[data-progress-percent]') !== '100%') throw new Error('progress rail did not reach 100%');
      if (text('[data-stage-label]') !== 'Test complete') throw new Error('progress rail label did not reach Test complete');
    }
    window.scrollTo(0, 0);
    return {
      status: text('#progressStatus'),
      download: text('#downloadSpeed'),
      upload: text('#uploadSpeed'),
      stageCount: stages.length,
      completedStages: stages.filter(stage => stage.dataset.outcome === 'succeeded').length,
      documentHeight: document.documentElement.scrollHeight
    };
  })()`;
}

function pngDimensions(buffer) {
  const signature = '89504e470d0a1a0a';
  if (buffer.subarray(0, 8).toString('hex') !== signature) throw new Error('captured file is not a PNG');
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

async function capturePage(devtools, baseURL, definition, outputDirectory) {
  await devtools.send('Emulation.setDeviceMetricsOverride', {
    width: definition.width,
    height: definition.height,
    deviceScaleFactor: 1,
    mobile: false,
    screenWidth: definition.width,
    screenHeight: definition.height
  });

  const loaded = devtools.waitFor('Page.loadEventFired');
  const navigation = await devtools.send('Page.navigate', { url: `${baseURL}/${definition.html}` });
  if (navigation.errorText) throw new Error(`${definition.html}: navigation failed: ${navigation.errorText}`);
  await loaded;
  await waitForApplication(devtools);

  await evaluate(devtools, `(() => {
    const style = document.createElement('style');
    style.dataset.screenshotCapture = 'true';
    style.textContent = ${JSON.stringify(captureCSS)};
    document.head.appendChild(style);
    document.documentElement.classList.add('screenshot-capture');
  })()`, `${definition.html}: install capture styles`);

  await evaluate(devtools, fixtureSource(), `${definition.html}: run deterministic measurement fixture`, true);
  await settlePage(devtools);
  const evidence = await evaluate(
    devtools,
    assertionSource(definition.html !== 'index.html'),
    `${definition.html}: verify rendered result`
  );
  await settlePage(devtools);

  const screenshot = await devtools.send('Page.captureScreenshot', {
    format: 'png',
    fromSurface: true,
    captureBeyondViewport: false,
    optimizeForSpeed: false,
    clip: {
      x: 0,
      y: 0,
      width: definition.width,
      height: definition.clipHeight,
      scale: 1
    }
  });
  const buffer = Buffer.from(screenshot.data, 'base64');
  const dimensions = pngDimensions(buffer);
  if (dimensions.width !== definition.width || dimensions.height !== definition.clipHeight) {
    throw new Error(`${definition.file}: expected ${definition.width}x${definition.clipHeight}, got ${dimensions.width}x${dimensions.height}`);
  }

  const outputPath = path.join(outputDirectory, definition.file);
  fs.writeFileSync(outputPath, buffer);
  const sha256 = crypto.createHash('sha256').update(buffer).digest('hex');
  console.log(`${definition.file}: ${dimensions.width}x${dimensions.height}, ${buffer.length} bytes, sha256 ${sha256}`);
  console.log(`  ${evidence.status}; ${evidence.download}/${evidence.upload} Mbps; ${evidence.completedStages}/${evidence.stageCount} staged outcomes; document ${evidence.documentHeight}px`);
}

async function stopBrowser(browser) {
  if (browser.exitCode !== null) return;
  browser.kill('SIGTERM');
  await Promise.race([
    new Promise(resolve => browser.once('exit', resolve)),
    new Promise(resolve => setTimeout(resolve, 3000))
  ]);
  if (browser.exitCode === null) browser.kill('SIGKILL');
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const browserPath = findBrowser(options.browser);
  const selectedCaptures = captures.filter(definition => {
    if (options.selected.size === 0) return true;
    const name = definition.file.replace('.png', '').replace('standard', 'standard').replace('observatory', 'observatory');
    return options.selected.has(name);
  });
  fs.mkdirSync(options.outputDirectory, { recursive: true });

  const server = createServer();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const baseURL = `http://127.0.0.1:${server.address().port}`;

  let launched;
  try {
    launched = await launchBrowser(browserPath);
    const { devtools } = launched;
    await devtools.send('Page.enable');
    await devtools.send('Runtime.enable');
    await devtools.send('Network.enable');
    await devtools.send('Network.setCacheDisabled', { cacheDisabled: true });
    await devtools.send('Network.setBlockedURLs', {
      urls: [
        'https://fonts.googleapis.com/*',
        'https://fonts.gstatic.com/*',
        'https://unpkg.com/*',
        'https://*.basemaps.cartocdn.com/*'
      ]
    });
    await devtools.send('Emulation.setTimezoneOverride', { timezoneId: 'America/Los_Angeles' });
    await devtools.send('Emulation.setLocaleOverride', { locale: 'en-US' });
    await devtools.send('Page.addScriptToEvaluateOnNewDocument', { source: preloadSource() });

    for (const definition of selectedCaptures) {
      await capturePage(devtools, baseURL, definition, options.outputDirectory);
    }
  } catch (error) {
    if (launched?.stderr && /ERR_BLOCKED_BY_ADMINISTRATOR/.test(launched.stderr())) {
      error.message += '\nChromium policy blocked the local capture URL; permit loopback URLs for this developer task.';
    }
    throw error;
  } finally {
    launched?.devtools.close();
    if (launched) await stopBrowser(launched.browser);
    if (launched?.profileDirectory) fs.rmSync(launched.profileDirectory, { recursive: true, force: true });
    await new Promise(resolve => server.close(resolve));
  }
}

main().catch(error => {
  console.error(error.stack || error.message || String(error));
  process.exitCode = 1;
});
