const { test, expect } = require('@playwright/test');

test('real browser loads the UI and verifies transfer receipts', async ({ page }) => {
    const pageErrors = [];
    page.on('pageerror', error => pageErrors.push(String(error)));

    await page.goto('/');
    await expect(page).toHaveTitle('NetSpeed - Network Speed Test');
    await expect(page.locator('#startTestBtn')).toBeVisible();

    const meta = await page.evaluate(async () => window.NetspeedApp.SpeedTest.fetchMeta());
    expect(meta.measurementProtocolVersion).toBe(2);
    expect(meta.uploadReceiptVersion).toBe(1);
    expect(meta.maxTransferBytes).toBe(16 * 1024 * 1024);

    const downloaded = await page.evaluate(async () => {
        const response = await fetch('/__down?bytes=262144&measId=browser-e2e-download', {
            cache: 'no-store'
        });
        if (!response.ok || !response.body) {
            throw new Error(`download failed: ${response.status}`);
        }
        const reader = response.body.getReader();
        let total = 0;
        for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            total += value.byteLength;
        }
        return {
            total,
            cacheControl: response.headers.get('cache-control'),
            contentType: response.headers.get('content-type')
        };
    });
    expect(downloaded.total).toBe(262144);
    expect(downloaded.cacheControl).toBe('no-store, no-transform');
    expect(downloaded.contentType).toContain('application/octet-stream');

    const receipt = await page.evaluate(async () => {
        const payload = new Uint8Array(262144);
        payload.fill(0xa5);
        const response = await fetch('/__up?measId=browser-e2e-upload', {
            method: 'POST',
            headers: { 'Content-Type': 'application/octet-stream' },
            body: payload
        });
        if (!response.ok) {
            throw new Error(`upload failed: ${response.status}`);
        }
        return response.json();
    });
    expect(receipt.ok).toBe(true);
    expect(receipt.acceptedBytes).toBe(262144);
    expect(receipt.serverDurationNs).toBeGreaterThan(0);
    expect(pageErrors).toEqual([]);
});

for (const variant of [
    {
        path: '/alternate.html',
        title: 'NetSpeed Observatory - Progressive Network Analysis',
        bodyClass: 'alternate-ui'
    },
    {
        path: '/phosphor.html',
        title: 'NetSpeed Phosphor - Link Analyzer',
        bodyClass: 'phosphor-ui'
    }
]) {
    test(`${variant.bodyClass} browser interface loads the complete measurement surface`, async ({ page }) => {
        const pageErrors = [];
        page.on('pageerror', error => pageErrors.push(String(error)));

        await page.goto(variant.path);
        await expect(page).toHaveTitle(variant.title);
        await expect(page.locator('body')).toHaveClass(new RegExp(variant.bodyClass));
        await expect(page.locator('#startTestBtn')).toBeVisible();
        await expect(page.locator('.progress-rail [data-progress-stage]')).toHaveCount(7);
        await expect(page.locator('.measurement-ledger')).toBeVisible();
        await expect(page.locator('#packetsReceived')).toBeVisible();
        if (variant.bodyClass === 'alternate-ui') await expect(page.locator('#detailsWorkspace')).toHaveAttribute('open', '');
        else await expect(page.locator('#detailsWorkspace')).not.toHaveAttribute('open', '');
        expect(pageErrors).toEqual([]);
    });
}

test('progress rail preserves unavailable packet-path outcome after completion', async ({ page }) => {
    test.setTimeout(90_000);
    const session = await page.context().newCDPSession(page);
    await session.send('Network.emulateNetworkConditions', { offline: false, latency: 5, downloadThroughput: 10000000, uploadThroughput: 2000000 });

    await page.route('**/meta', async route => {
        const response = await route.fetch();
        const meta = await response.json();
        meta.packetLossFrameVersion = 0;
        meta.maxTransferBytes = 1_000_000;
        meta.maxConcurrentTransfersPerClient = 2;
        await route.fulfill({ response, json: meta });
    });

    await page.goto('/alternate.html');
    await page.evaluate(() => {
        window.NetspeedApp.SpeedTest.CONFIG.latencyProbes = 3;
        window.NetspeedApp.SpeedTest.CONFIG.loadedLatencyProbes = 3;
        window.NetspeedApp.SpeedTest.CONFIG.packetLossPackets = 3;
        window.NetspeedApp.SpeedTest.CONFIG.packetLossInterval = 0;
        window.NetspeedApp.SpeedTest.CONFIG.packetLossExtraWait = 0;
    });

    await page.locator('#startTestBtn').click();
    await expect(page.locator('#progressStatus')).toHaveText('Test complete', { timeout: 75_000 });

    const packetStage = page.locator('[data-progress-stage="packet-loss"]');
    await expect(packetStage).toHaveAttribute('data-outcome', 'unavailable');
    await expect(packetStage).toHaveClass(/is-unavailable/);
    await expect(packetStage).not.toHaveClass(/is-complete/);
    await expect(page.locator('[data-progress-stage="complete"]')).toHaveAttribute('data-outcome', 'succeeded');
    await expect(page.locator('#packetLossValue')).toHaveText('Not measured');
    await expect(page.locator('#packetLossDetail')).toContainText('Unable to perform measurement');
    await expect(page.locator('#measurementNotes')).toBeVisible();

    const outcomes = await page.evaluate(() => window.NetspeedApp.state.stageOutcomes);
    expect(outcomes['packet-loss'].outcome).toBe('unavailable');
    expect(outcomes.complete.outcome).toBe('succeeded');
});

test('progress rail preserves a failed transfer stage instead of completing the sequence', async ({ page }) => {
    test.setTimeout(60_000);

    await page.route(/\/__down(?:\?|$)/, async route => {
        const url = new URL(route.request().url());
        if (url.searchParams.get('bytes') === '0') {
            await route.continue();
            return;
        }
        await route.fulfill({
            status: 503,
            contentType: 'text/plain',
            body: 'deliberate download failure'
        });
    });

    await page.goto('/alternate.html');
    await page.evaluate(() => {
        window.NetspeedApp.SpeedTest.CONFIG.latencyProbes = 3;
        window.NetspeedApp.SpeedTest.CONFIG.loadedLatencyProbes = 3;
    });

    await page.locator('#startTestBtn').click();
    await expect(page.locator('#progressStatus')).toHaveText('Test failed', { timeout: 45_000 });

    await expect(page.locator('[data-progress-stage="download"]')).toHaveAttribute('data-outcome', 'failed');
    await expect(page.locator('[data-progress-stage="download"]')).toHaveClass(/is-failed/);
    await expect(page.locator('[data-progress-stage="packet-loss"]')).toHaveAttribute('data-outcome', 'pending');
    await expect(page.locator('[data-progress-stage="packet-loss"]')).not.toHaveClass(/is-complete/);
    await expect(page.locator('[data-progress-stage="complete"]')).toHaveAttribute('data-outcome', 'failed');
});

test('shared-result state survives switching among all presentation variants', async ({ page }) => {
    await page.route('https://unpkg.com/**', route => route.abort());
    await page.addInitScript(() => {
        Object.defineProperty(navigator, 'share', {
            configurable: true,
            value: async data => { window.__netspeedSharedURL = data.url; }
        });
    });

    await page.goto('/index.html');
    await page.waitForFunction(() => Boolean(window.NetspeedApp.state.meta));
    await page.evaluate(() => {
        const state = window.NetspeedApp.state;
        state.summary = {
            downloadMbps: 123.4,
            uploadMbps: 45.6,
            latencyUnloadedMs: 12.3,
            jitterMs: 1.2,
            packetLossPercent: 0.2
        };
        state.quality = {
            videoStreaming: 'Great',
            gaming: 'Great',
            videoChatting: 'Great'
        };
        state.packetLoss = {
            lossPercent: 0.2,
            sent: 1000,
            received: 998,
            rttStatsMs: { min: 10, median: 12, p90: 15 },
            jitterMs: 3
        };
        state.downloadSamples = [120, 123.4, 126].map(mbps => ({ mbps }));
        state.uploadSamples = [44, 45.6, 47].map(mbps => ({ mbps }));
        state.latencySamples = [
            { condition: 'unloaded', rttMs: 11 },
            { condition: 'unloaded', rttMs: 12 },
            { condition: 'unloaded', rttMs: 13 },
            { condition: 'download', rttMs: 20 },
            { condition: 'download', rttMs: 21 },
            { condition: 'upload', rttMs: 22 },
            { condition: 'upload', rttMs: 23 }
        ];
        state.meta = { colo: 'RDM', latitude: 44.05, longitude: -121.31 };
        state.locations = [{ iata: 'RDM', city: 'Redmond', cca2: 'US', lat: 44.27, lon: -121.17 }];
        document.getElementById('shareBtn').disabled = false;
    });
    await page.locator('#shareBtn').click();
    const sharedURL = await page.evaluate(() => window.__netspeedSharedURL);
    expect(sharedURL).toBeTruthy();
    const shared = new URL(sharedURL);
    expect(shared.searchParams.get('r')).toBeTruthy();

    shared.pathname = '/alternate.html';
    await page.goto(shared.href);
    await expect(page.locator('#downloadSpeed')).toHaveText('123.4');
    const encoded = new URL(page.url()).searchParams.get('r');

    const targetPaths = {
        phosphor: '/phosphor.html',
        standard: '/', // net/http's FileServer canonicalizes index.html to ./.
        alternate: '/alternate.html'
    };
    for (const target of ['phosphor', 'standard', 'alternate']) {
        await Promise.all([
            page.waitForURL(url => url.pathname === targetPaths[target] && url.searchParams.get('r') === encoded),
            page.locator(`[data-interface-link="${target}"]`).first().click()
        ]);
        expect(new URL(page.url()).searchParams.get('r')).toBe(encoded);
        await expect(page.locator('#downloadSpeed')).toHaveText('123.4');
    }
});

for (const path of ['/index.html', '/alternate.html', '/phosphor.html']) {
    test(`${path} evidence is lossless, keyboard accessible, and responsive`, async ({ page }) => {
        await page.route('https://unpkg.com/**', route => route.abort());
        await page.goto(path);
        await page.waitForFunction(() => Boolean(window.NetspeedApp && window.NetspeedEvidence));
        await page.evaluate(async () => {
            await document.fonts.ready;
            window.NetspeedEvidence.setResults({
                meta: { measurementProtocolVersion: 2 },
                latencySamples: [{ condition: 'unloaded', rawRttMs: 0, rttMs: null, timingResolutionLimited: true }],
                packetLoss: { unavailable: true, reason: 'TURN relay unavailable' },
                futureTelemetry: { zero: 0, disabled: false, missing: null, text: '<img src=x onerror=alert(1)>' }
            });
        });
        await expect(page.locator('#measurementNotes')).toContainText('Browser timer resolution limited 1 latency sample');
        await expect(page.locator('#measurementNotes')).toContainText('TURN relay unavailable');
        await page.getByRole('button', { name: 'View details ↓', exact: true }).click();
        await page.locator('#tab-latency').click();
        await page.keyboard.press('ArrowRight');
        await expect(page.locator('#tab-packet')).toBeFocused();
        await expect(page.locator('#panel-packet')).toBeVisible();
        await page.keyboard.press('End');
        await expect(page.locator('#tab-raw')).toBeFocused();
        await expect(page.locator('#rawEvidence img')).toHaveCount(0);
        const raw = await page.locator('#rawEvidence').textContent();
        expect(JSON.parse(raw).futureTelemetry).toEqual({ zero: 0, disabled: false, missing: null, text: '<img src=x onerror=alert(1)>' });

        for (const width of [1440, 768, 390, 320]) {
            await page.setViewportSize({ width, height: 1200 });
            for (const theme of ['light', 'dark']) {
                await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
                expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
            }
            if (path === '/phosphor.html') {
                for (const columns of ['132', '80']) {
                    await page.locator('#terminalColumns').selectOption(columns);
                    await expect(page.locator('body')).toHaveAttribute('data-columns', columns);
                    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
                }
            }
        }
        if (path === '/phosphor.html') {
            await page.keyboard.press('F2');
            await expect(page.locator('#panel-throughput')).toBeVisible();
            await page.keyboard.press('Escape');
            await expect(page.locator('#detailsWorkspace')).not.toHaveAttribute('open', '');
        }
    });
}
