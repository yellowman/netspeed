'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = path.resolve(__dirname, '..', '..');
const app = fs.readFileSync(path.join(root, 'web/js/app.js'), 'utf8');
const requiredIds = new Set(
    Array.from(app.matchAll(/getElementById\('([^']+)'\)/g), match => match[1])
);

const diagnosticIds = [
    'packetsReceived',
    'rttMin',
    'rttP90',
    'maxBurst',
    'avgBurst',
    'downloadVariability',
    'uploadVariability',
    'scoreDescription'
];

for (const pageName of ['alternate.html', 'phosphor.html']) {
    const html = fs.readFileSync(path.join(root, 'web', pageName), 'utf8');
    const ids = Array.from(html.matchAll(/\bid="([^"]+)"/g), match => match[1]);
    const counts = new Map();
    for (const id of ids) counts.set(id, (counts.get(id) || 0) + 1);

    const duplicates = Array.from(counts.entries()).filter(([, count]) => count > 1);
    assert.deepEqual(duplicates, [], `${pageName} must not contain duplicate IDs`);

    for (const id of requiredIds) {
        assert.equal(counts.get(id), 1, `${pageName} must expose app element #${id}`);
    }
    for (const id of diagnosticIds) {
        assert.match(html, new RegExp(`measurement-ledger[\\s\\S]*id="${id}"`),
            `${pageName} must visibly include diagnostic #${id}`);
    }

    assert.match(html, /<details class="extras-details" open>/,
        `${pageName} must expose extended measurements by default`);
    assert.equal((html.match(/data-progress-stage=/g) || []).length, 7,
        `${pageName} must expose the complete progressive test sequence`);
    assert.match(html, /js\/interface\.js/,
        `${pageName} must load the progressive interface helper`);
}

const phosphor = fs.readFileSync(path.join(root, 'web/phosphor.html'), 'utf8');
assert.doesNotMatch(phosphor, /fonts\.googleapis|fonts\.gstatic/,
    'phosphor.html must use the local terminal font stack');
assert.match(phosphor, /css\/phosphor\.css/);
assert.match(fs.readFileSync(path.join(root, 'web/css/phosphor.css'), 'utf8'), /repeating-linear-gradient/,
    'phosphor interface must include scanline/terminal rendering');

console.log('alternate browser interfaces validated');
