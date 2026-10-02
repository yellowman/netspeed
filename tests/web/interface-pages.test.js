'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '..', '..');
const { render } = require('../../web/js/layout.js');
const app = fs.readFileSync(path.join(root, 'web/js/app.js'), 'utf8');
const requiredIds = new Set(Array.from(app.matchAll(/getElementById\('([^']+)'\)/g), match => match[1]));
const styles = fs.readFileSync(path.join(root, 'web/css/styles.css'), 'utf8');

for (const [pageName, variant] of [['index.html', 'standard'], ['alternate.html', 'alternate'], ['phosphor.html', 'phosphor']]) {
    const bootstrap = fs.readFileSync(path.join(root, 'web', pageName), 'utf8');
    assert.match(bootstrap, new RegExp(`data-interface="${variant}"`));
    assert.equal((bootstrap.match(/css\/[^" ]+\.css/g) || []).length, 1, 'each presentation uses the same stylesheet');
    const scripts = ['layout', 'charts', 'http_transport', 'speedtest', 'interface', 'evidence', 'app'];
    let previous = -1;
    for (const name of scripts) {
        const index = bootstrap.indexOf(`js/${name}.js`);
        assert.ok(index > previous, `${pageName}: ${name} must load once, in dependency order`);
        assert.equal((bootstrap.match(new RegExp(`js/${name}\\.js`, 'g')) || []).length, 1);
        previous = index;
    }
    const html = render(variant);
    const counts = new Map();
    for (const match of html.matchAll(/\bid="([^"]+)"/g)) counts.set(match[1], (counts.get(match[1]) || 0) + 1);
    assert.deepEqual([...counts].filter(([, count]) => count > 1), [], `${variant}: unique DOM bindings`);
    for (const id of requiredIds) assert.equal(counts.get(id), 1, `${variant}: app binding #${id}`);
    for (const component of ['app-shell', 'result-header', 'metric', 'measurement-figure', 'stage-rail', 'evidence-table', 'details-section', 'action-bar']) assert.ok(html.includes(component), `${variant}: shared component ${component}`);
    assert.equal((html.match(/data-progress-stage=/g) || []).length, 7);
    assert.equal((html.match(/role="tab"/g) || []).length, 6);
    assert.equal((html.match(/role="tabpanel"/g) || []).length, 6);
    assert.match(html, /id="detailsWorkspace"/);
    assert.match(html, /id="rawEvidence"/);
    assert.match(html, /id="measurementNotes"[^>]*hidden/);
    for (const name of ['standard', 'alternate', 'phosphor']) assert.ok(html.includes(`data-interface-link="${name}"`));
    assert.doesNotMatch(html, /APPLE|PRODOS|BLOAD|64K RAM|SYSTEM DISK|View on GitHub|accordion-label/);
    assert.doesNotMatch(bootstrap, /fonts\.googleapis|fonts\.gstatic|apple2|alternate\.css|phosphor\.css/);
    if (variant === 'phosphor') {
        assert.match(html, /id="terminalColumns"/);
        assert.match(html, /value="80"/); assert.match(html, /value="132"/);
        assert.match(html, /F1 Details/); assert.match(html, /F4 Packet/);
        assert.match(styles, /GlassTTYVT220/);
    }
    if (variant === 'alternate') {
        assert.match(html, /class="instrument-readout"/);
        assert.doesNotMatch(html, /class="primary-metrics"/);
        assert.match(html, /id="acquisitionLedger"/);
        assert.match(html, /Measurement console/);
        assert.match(html, /Measurement inspector/);
        assert.match(html, /id="recentEvents"/);
        assert.match(html, /Observation stream/);
        assert.doesNotMatch(html, /id="connectionRibbon"/);
    } else {
        assert.doesNotMatch(html, /class="instrument-readout"|id="acquisitionLedger"/);
    }
    if (variant === 'standard') {
        assert.match(html, /id="connectionRibbon"/);
        assert.match(html, /id="measurementSignature"/);
        assert.match(html, /class="hero-status-track"/);
    }
    if (variant === 'phosphor') assert.doesNotMatch(html, /hero-status-track|connectionRibbon|measurementSignature|recentEvents/);
    assert.match(html, /id="eventHistory"/);
}
assert.throws(() => render('unknown'), /Unknown presentation/);
// DESIGN_LANGUAGE.md permits presentation-scoped lighting, depth, and chart
// treatments. Guard oversized decorative radii, not the presence of CSS effects.
assert.doesNotMatch(styles, /border-radius:\s*(16|30)px/);
for (const category of ['download', 'upload', 'latency', 'jitter', 'packet-loss']) assert.match(styles, new RegExp(`--color-${category}: var\\(--accent\\)`));
assert.match(styles, /prefers-reduced-motion/);
const helper = fs.readFileSync(path.join(root, 'web/js/interface.js'), 'utf8');
assert.doesNotMatch(helper, /stageFromStatus|progressStatus.*MutationObserver/s);
assert.match(helper, /netspeed:stagechange/);
for (const outcome of ['pending', 'running', 'succeeded', 'unavailable', 'failed']) assert.ok(helper.includes(`'${outcome}'`));
assert.match(helper, /params\.get\('r'\)/);
assert.match(helper, /target\.search = ''/);
console.log('shared browser presentation contracts passed');
