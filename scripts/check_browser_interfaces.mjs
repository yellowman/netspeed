#!/usr/bin/env node
/* Native Chromium/CDP qualification, including platforms unsupported by Playwright. */
import assert from 'node:assert/strict';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { findBrowser, launchBrowser, stopBrowser, evaluate, waitForApplication, settlePage, fixtureSource, preloadSource } from './capture_interfaces.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const probe = net.createServer();
await new Promise(resolve => probe.listen(0, '127.0.0.1', resolve));
const port = probe.address().port;
await new Promise(resolve => probe.close(resolve));
const baseURL = `http://127.0.0.1:${port}`;
const buildDirectory = fs.mkdtempSync(path.join(os.tmpdir(), 'netspeed-browser-check-'));
const daemonPath = path.join(buildDirectory, process.platform === 'win32' ? 'netspeedd.exe' : 'netspeedd');
let daemon;
let daemonLog = '';
let launched;
try {
    const build = spawnSync(process.env.GO || 'go', ['build', '-o', daemonPath, './cmd/netspeedd'], { cwd: root, encoding: 'utf8', timeout: 120000 });
    if (build.error || build.status !== 0) throw new Error(`daemon build failed: ${build.error || build.stderr}`);
    // Run the binary directly so teardown owns the daemon, not a go-run parent.
    daemon = spawn(daemonPath, ['--listen', `127.0.0.1:${port}`, '--web-dir', './web', '--cors=false', '--max-bytes', '16777216', '--max-transfers', '64', '--max-client-transfers', '16', '--client-quota-bytes', '0'], { cwd: root, stdio: ['ignore', 'ignore', 'pipe'] });
    daemon.stderr.on('data', chunk => { daemonLog += chunk; });
    const deadline = Date.now() + 120000;
    for (;;) {
        if (daemon.exitCode !== null) throw new Error(`daemon failed: ${daemonLog}`);
        try { if ((await fetch(`${baseURL}/health`)).ok) break; } catch (_) {}
        if (Date.now() > deadline) throw new Error(`daemon startup timed out: ${daemonLog}`);
        await pause(100);
    }
    launched = await launchBrowser(findBrowser(process.env.NETSPEED_BROWSER));
    const { devtools } = launched;
    for (const domain of ['Page', 'Runtime', 'Network']) await devtools.send(`${domain}.enable`);
    await devtools.send('Network.setBlockedURLs', { urls: ['https://unpkg.com/*', 'https://*.basemaps.cartocdn.com/*'] });
    const navigate = async page => {
        devtools.exceptions.length = 0;
        const loaded = devtools.waitFor('Page.loadEventFired');
        const navigation = await devtools.send('Page.navigate', { url: `${baseURL}/${page}` });
        if (navigation.errorText) throw new Error(navigation.errorText);
        await loaded; await waitForApplication(devtools);
    };
    await navigate('index.html');
    await evaluate(devtools, `(async () => {
        const meta = await NetspeedApp.SpeedTest.fetchMeta();
        if (meta.measurementProtocolVersion !== 2 || meta.uploadReceiptVersion !== 1) throw new Error('unsupported server contract');
        const down = await fetch('/__down?bytes=262144');
        if ((await down.arrayBuffer()).byteLength !== 262144 || down.headers.get('cache-control') !== 'no-store, no-transform') throw new Error('download contract');
        const up = await fetch('/__up', { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: new Uint8Array(262144) });
        const receipt = await up.json();
        if (!receipt.ok || receipt.acceptedBytes !== 262144 || receipt.serverDurationNs <= 0) throw new Error('upload receipt');
    })()`, 'real HTTP transfer receipts', true);
    console.log('native Chromium: real HTTP contracts passed');

    await navigate('alternate.html');
    // A loopback path is too fast for reliable body/probe overlap with tiny
    // transfers. Shape this actual-network fixture, not the measured samples.
    await devtools.send('Network.emulateNetworkConditions', { offline: false, latency: 5, downloadThroughput: 10000000, uploadThroughput: 2000000 });
    await evaluate(devtools, `(async () => {
        const nativeFetch = window.fetch;
        window.fetch = async (...args) => {
            const response = await nativeFetch(...args);
            if (new URL(String(args[0]), location.href).pathname !== '/meta') return response;
            const meta = await response.json();
            meta.packetLossFrameVersion = 0; meta.maxTransferBytes = 1000000; meta.maxConcurrentTransfersPerClient = 2;
            return new Response(JSON.stringify(meta), { headers: { 'Content-Type': 'application/json' } });
        };
        NetspeedApp.SpeedTest.CONFIG.latencyProbes = 3;
        NetspeedApp.SpeedTest.CONFIG.loadedLatencyProbes = 3;
        await NetspeedApp.startTest();
        if (document.querySelector('#progressStatus').textContent !== 'Test complete') throw new Error('actual test failed: ' + JSON.stringify(NetspeedApp.state.evidence?.error));
        const packet = document.querySelector('[data-progress-stage="packet-loss"]');
        if (packet.dataset.outcome !== 'unavailable' || packet.classList.contains('is-complete')) throw new Error('unavailable packet path misreported');
        if (document.querySelector('#packetLossValue').textContent !== 'Not measured') throw new Error('missing unavailable explanation');
        const raw = JSON.parse(document.querySelector('#rawEvidence').textContent);
        if (!raw.throughputSamples.some(s => s.transfers?.length > 1) || !raw.packetLoss.unavailable || !raw.httpTransport) throw new Error('incomplete raw evidence');
        if (!raw.throughputSamples.some(s => s.transfers?.some(t => t.receipt?.acceptedBytes > 0))) throw new Error('lost upload receipts');
        if (!raw.measurementEvents.some(e => e.type === 'window') || !raw.measurementEvents.some(e => e.type === 'latency') || raw.measurementEvents.some(e => e.source !== 'client-progress-callback')) throw new Error('actual measurement observation history lost');
        if (document.querySelectorAll('#recentEvents .event-line').length !== 4) throw new Error('unbounded live evidence stream');
        if (document.querySelector('#measurementNotes').hidden) throw new Error('missing measurement notes');
    })()`, 'actual measurement and unavailable packet stage', true);
    console.log('native Chromium: real engine, transfer evidence, and unavailable stage passed');
    await devtools.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });

    await navigate('alternate.html');
    await evaluate(devtools, `(async () => {
        const nativeFetch = window.fetch;
        window.fetch = async (...args) => {
            const url = new URL(String(args[0]), location.href);
            if (url.pathname === '/__down' && url.searchParams.get('bytes') !== '0') return new Response('deliberate failure', { status: 503 });
            return nativeFetch(...args);
        };
        NetspeedApp.SpeedTest.CONFIG.latencyProbes = 3;
        await NetspeedApp.startTest();
        if (document.querySelector('#progressStatus').textContent !== 'Test failed') throw new Error('test did not fail');
        if (document.querySelector('[data-progress-stage="download"]').dataset.outcome !== 'failed') throw new Error('failed stage lost');
        const raw = JSON.parse(document.querySelector('#rawEvidence').textContent);
        if (!raw.error || !raw.measurementErrors.length) throw new Error('partial failure evidence lost');
        if (!raw.measurementEvents.some(e => e.type === 'stage' && e.outcome === 'failed')) throw new Error('failed observation history lost');
    })()`, 'actual failed measurement preserves partial evidence', true);
    console.log('native Chromium: failed stage and partial raw evidence passed');

    for (const page of ['index.html', 'alternate.html', 'phosphor.html']) {
        for (const width of [1440, 768, 390, 320]) {
            await devtools.send('Emulation.setDeviceMetricsOverride', { width, height: 1200, deviceScaleFactor: 1, mobile: width < 640 });
            await navigate(page);
            await evaluate(devtools, preloadSource(), 'offline map and clock fixture');
            await evaluate(devtools, fixtureSource(), `${page}: render fixture`, true);
            await settlePage(devtools);
            await evaluate(devtools, `(() => {
                const assert = (ok, reason) => { if (!ok) throw new Error(reason); };
                const ids = [...document.querySelectorAll('[id]')].map(n => n.id);
                document.dispatchEvent(new MouseEvent('mouseenter'));
                document.dispatchEvent(new MouseEvent('mouseleave'));
                assert(ids.length === new Set(ids).size, 'duplicate DOM IDs');
                assert(document.documentElement.scrollWidth <= innerWidth, 'horizontal overflow');
                const line = document.querySelector('#downloadSparkline .sparkline-line');
                if (document.body.dataset.interface === 'phosphor') assert(document.querySelector('#downloadSparkline .terminal-plot')?.textContent.includes('*'), 'missing terminal plot');
                else assert(line && getComputedStyle(line).stroke !== 'none', 'missing throughput line');
                assert(document.querySelector('#measurementNotes').hidden, 'spurious warning strip');
                assert(document.querySelector('#detailsWorkspace').open === (document.body.dataset.interface === 'alternate'), 'wrong initial details state');
                if (document.body.dataset.interface === 'alternate') {
                    const readout = document.querySelector('.instrument-readout');
                    assert(readout && !document.querySelector('.primary-metrics'), 'Observatory must not reuse the Standard hero');
                    assert(document.querySelector('#inspectorTitle').textContent === 'Download / evidence', 'initial Download inspector');
                    assert(document.querySelectorAll('#acquisitionLedger tbody tr').length === 6, 'live window ledger');
                    assert(document.querySelector('.instrument-readout [data-direction="download"]').getAttribute('aria-pressed') === 'true', 'initial inspection selection');
                    const chart = document.querySelector('#downloadSparkline svg');
                    chart.focus(); chart.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }));
                    assert(chart.dataset.selectedSample === '0', 'keyboard chart sample selection');
                    assert(document.querySelector('#downloadMeasurementStrip').textContent.includes('window 1/3'), 'recorded window strip');
                    assert(document.querySelector('#inspectorContent').textContent.includes('Selected window'), 'selected window inspector');
                    assert(document.querySelector('[data-measurement="throughput"][data-direction="download"]').classList.contains('is-inspected'), 'illuminated measurement selection');
                    assert(document.querySelectorAll('#recentEvents .event-line').length === 4, 'bounded recent observation stream');
                    const recorded = JSON.parse(document.querySelector('#rawEvidence').textContent).measurementEvents;
                    assert(recorded.length > 20 && recorded.every(e => Number.isFinite(e.observedAt) && e.source === 'client-progress-callback'), 'actual callback history retained in raw evidence');
                    if (innerWidth > 1000) {
                        assert(readout.getBoundingClientRect().height <= 120, 'compact instrument readout');
                        assert(document.querySelector('.evidence-inspector').getBoundingClientRect().left >= document.querySelector('.measurement-content').getBoundingClientRect().right, 'two-pane console');
                    }
                }
                if (document.body.dataset.interface === 'standard') {
                    assert(document.querySelector('#downloadSpeed .metric-fraction').textContent === '.7', 'tabular decimal fraction');
                    assert(document.querySelector('#downloadSpeed').textContent === '486.7', 'styling changed the measured number');
                    assert(document.querySelector('.hero-status-track').parentElement.classList.contains('result-header'), 'status line escaped result composition');
                    assert(document.querySelector('.hero-status-track').getBoundingClientRect().width > 200, 'full-width signature status line');
                    assert(document.querySelector('#connectionRibbon').textContent.includes('HTTP throughput') && document.querySelector('#connectionRibbon').textContent.includes('Packet delivery'), 'separate observed transport branches');
                }
                document.querySelector('[data-open-evidence="latency"]').click();
                assert(document.querySelector('#detailsWorkspace').open && !document.querySelector('#panel-latency').hidden, 'latency evidence did not open');
                const tab = document.querySelector('#tab-latency');
                tab.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
                assert(document.activeElement.id === 'tab-packet' && !document.querySelector('#panel-packet').hidden, 'keyboard tabs');
                tab.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }));
                assert(document.activeElement.id === 'tab-raw' && !document.querySelector('#panel-raw').hidden, 'raw evidence tab');
                const raw = JSON.parse(document.querySelector('#rawEvidence').textContent);
                raw.futureTelemetry = { zero: 0, disabled: false, missing: null, text: '<img src=x onerror=alert(1)>' };
                NetspeedEvidence.setResults(raw);
                assert(JSON.parse(document.querySelector('#rawEvidence').textContent).futureTelemetry.zero === 0, 'unknown telemetry lost');
                assert(!document.querySelector('#rawEvidence img'), 'raw evidence interpreted as HTML');
                if (document.body.dataset.interface === 'alternate') {
                    document.querySelector('[data-progress-stage="download"] button').click();
                    assert(document.querySelector('#inspectorTitle').textContent === 'Download / evidence', 'stage inspector selection');
                    document.querySelector('.instrument-readout [data-direction="upload"]').click();
                    assert(document.querySelector('#inspectorTitle').textContent === 'Upload / evidence', 'readout inspector selection');
                    assert(document.querySelector('.instrument-readout [data-direction="upload"]').getAttribute('aria-pressed') === 'true', 'selected readout accessibility');
                    assert(document.querySelector('.instrument-readout [data-direction="download"]').getAttribute('aria-pressed') === 'false', 'stale readout selection');
                    document.querySelector('#inspectorDetailsBtn').click();
                    assert(document.querySelector('#inspectorTitle').textContent === 'Upload / evidence' && !document.querySelector('#panel-throughput').hidden, 'inspector detail link lost selection');
                }
                if (document.body.dataset.interface === 'phosphor') {
                    const columns = document.querySelector('#terminalColumns');
                    for (const value of ['132', '80']) {
                        columns.value = value; columns.dispatchEvent(new Event('change'));
                        assert(document.body.dataset.columns === value, 'terminal column selection');
                        assert(document.documentElement.scrollWidth <= innerWidth, 'terminal column overflow');
                    }
                    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'F2', bubbles: true }));
                    assert(!document.querySelector('#panel-throughput').hidden, 'F2 throughput');
                    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
                    assert(!document.querySelector('#detailsWorkspace').open, 'Escape returns to results');
                }
                for (const theme of ['light', 'dark']) {
                    document.documentElement.dataset.theme = theme;
                    assert(document.documentElement.scrollWidth <= innerWidth, theme + ' evidence overflow');
                }
                assert(document.querySelectorAll('[role="tab"][aria-selected="true"]').length === 1, 'ambiguous selected tab');
                // Partial evidence and warning text must also fit, not only the
                // fully populated screenshot fixture.
                NetspeedEvidence.setResults({
                    meta: { measurementProtocolVersion: 2 },
                    latencySamples: [{ condition: 'unloaded', rawRttMs: 0, rttMs: null, timingResolutionLimited: true }],
                    packetLoss: { unavailable: true, reason: 'TURN relay unavailable' },
                    futureTelemetry: { zero: 0, disabled: false, missing: null }
                });
                assert(document.documentElement.scrollWidth <= innerWidth, 'partial evidence overflow');
            })()`, `${page}: ${width}px responsive evidence controls`);
            assert.deepEqual(devtools.exceptions, [], `${page}: unexpected page errors`);
            console.log(`native Chromium: ${page} at ${width}px, both themes, keyboard and raw evidence passed`);
        }
    }
    await navigate('index.html');
    await evaluate(devtools, preloadSource(), 'shared offline map fixture');
    await evaluate(devtools, fixtureSource(), 'shared fixture', true);
    const sharedURL = await evaluate(devtools, `(async () => {
        Object.defineProperty(navigator, 'share', { configurable: true, value: async data => { window.sharedURL = data.url; } });
        document.querySelector('#shareBtn').click();
        for (let i = 0; i < 100 && !window.sharedURL; i++) await new Promise(resolve => setTimeout(resolve, 10));
        return window.sharedURL;
    })()`, 'generate real compact share link', true);
    assert.ok(new URL(sharedURL).searchParams.get('r'));
    for (const page of ['index.html', 'alternate.html', 'phosphor.html']) {
        const url = new URL(sharedURL); url.pathname = `/${page}`;
        const loaded = devtools.waitFor('Page.loadEventFired');
        await devtools.send('Page.navigate', { url: url.href }); await loaded; await waitForApplication(devtools);
        await evaluate(devtools, `(() => {
            if (document.querySelector('#downloadSpeed').textContent !== '486.7') throw new Error('shared result lost');
            if (!JSON.parse(document.querySelector('#rawEvidence').textContent).sharedResult) throw new Error('compact evidence subset not disclosed');
            for (const link of document.querySelectorAll('[data-interface-link]')) if (!new URL(link.href).searchParams.get('r')) throw new Error('share parameter lost on presentation link');
        })()`, `${page}: shared result`);
    }
    for (const variant of ['standard', 'alternate', 'phosphor']) {
        const loaded = devtools.waitFor('Page.loadEventFired');
        await evaluate(devtools, `document.querySelector('.app-footer [data-interface-link="${variant}"]').click()`, 'switch shared presentation');
        await loaded; await waitForApplication(devtools);
        await evaluate(devtools, `(() => { if (!location.search.includes('r=') || document.querySelector('#downloadSpeed').textContent !== '486.7') throw new Error('presentation switch lost shared result'); })()`, 'shared presentation roundtrip');
    }
    console.log('native Chromium: shared-result identity and presentation switching passed');
} catch (error) {
    error.message += '\nBrowser exceptions:\n' + (launched?.devtools.exceptions || []).join('\n');
    throw error;
} finally {
    launched?.devtools.close();
    if (launched) await stopBrowser(launched.browser);
    if (launched?.profileDirectory) fs.rmSync(launched.profileDirectory, { recursive: true, force: true });
    if (daemon && daemon.exitCode === null && daemon.signalCode === null) {
        daemon.kill('SIGTERM');
        await new Promise(resolve => {
            const timer = setTimeout(() => { daemon.kill('SIGKILL'); resolve(); }, 3000);
            daemon.once('exit', () => { clearTimeout(timer); resolve(); });
        });
    }
    fs.rmSync(buildDirectory, { recursive: true, force: true });
}
