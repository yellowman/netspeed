#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const root = path.resolve(__dirname, '..', '..');
const script = fs.readFileSync(path.join(root, 'scripts', 'capture_interfaces.mjs'), 'utf8');
const interfaceJS = fs.readFileSync(path.join(root, 'web', 'js', 'interface.js'), 'utf8');
const phosphorJS = fs.readFileSync(path.join(root, 'web', 'js', 'phosphor-apple2.js'), 'utf8');
const phosphorCSS = fs.readFileSync(path.join(root, 'web', 'css', 'phosphor-apple2.css'), 'utf8');
const readme = fs.readFileSync(path.join(root, 'README.md'), 'utf8');

for (const required of [
  "file: 'standard.png'",
  "file: 'observatory.png'",
  "file: 'phosphor.png'",
  'Page.captureScreenshot',
  'window.NetspeedApp.startTest()',
  'callbacks.onMetaReceived',
  'callbacks.onLatencyProgress',
  'callbacks.onDownloadProgress',
  'callbacks.onUploadProgress',
  'callbacks.onPacketLossProgress',
  'callbacks.onComplete',
  'Network.setBlockedURLs',
  'measurementCapabilities: capabilities'
]) {
  if (!script.includes(required)) throw new Error(`capture script missing: ${required}`);
}

for (const forbidden of [
  "querySelectorAll('[id], [class]')",
  "querySelectorAll('[hidden], .hidden",
  "getContext('2d')",
  "classList.add('test-complete'",
  "require('playwright')",
  "require('puppeteer')"
]) {
  if (script.includes(forbidden)) throw new Error(`capture script still fabricates presentation state: ${forbidden}`);
}

if (!/\.hero-metrics-row\s*\{[^}]*repeat\(5,minmax\(0,1fr\)\)/s.test(phosphorCSS)) {
  throw new Error('Apple II stylesheet does not preserve the five-metric hero row');
}
if (/\[class\*="progress"\]/.test(phosphorCSS)) {
  throw new Error('Apple II stylesheet still treats the structured progress rail as a meter');
}
if (/\[class\*="progress"\]/.test(phosphorJS)) {
  throw new Error('Apple II adapter still inserts text meters after every structured progress element');
}
if (!/querySelectorAll\('\[data-live-clock\]'\)/.test(interfaceJS)) {
  throw new Error('presentation adapter does not populate live clocks');
}
if (!readme.includes('node scripts/capture_interfaces.mjs')) {
  throw new Error('README does not document deterministic screenshot regeneration');
}

console.log('screenshot capture contract ok');
