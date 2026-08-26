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
