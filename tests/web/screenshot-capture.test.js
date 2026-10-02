#!/usr/bin/env node
'use strict';

const fs = require('node:fs');
const path = require('node:path');

const root = path.resolve(__dirname, '..', '..');
const script = fs.readFileSync(path.join(root, 'scripts', 'capture_interfaces.mjs'), 'utf8');
const interfaceJS = fs.readFileSync(path.join(root, 'web', 'js', 'interface.js'), 'utf8');
const sharedCSS = fs.readFileSync(path.join(root, 'web', 'css', 'styles.css'), 'utf8');
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
if (!script.includes('Observatory reverted to Standard hero layout') || !script.includes('screenshot would crop the primary packet measurement')) {
  throw new Error('capture must verify distinct composition and uncropped primary measurements');
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

if (!sharedCSS.includes('.primary-metrics') || !sharedCSS.includes('.details-workspace')) throw new Error('missing shared result and evidence layout');
if (!script.includes("'/usr/local/bin/chrome'")) throw new Error('capture tool must find OpenBSD Chromium');
if (!script.includes('.sparkline-line { stroke-dashoffset: 0')) throw new Error('capture must reveal chart paths when animation is disabled');
if (!/querySelectorAll\('\[data-live-clock\]'\)/.test(interfaceJS)) {
  throw new Error('presentation adapter does not populate live clocks');
}
if (!readme.includes('node scripts/capture_interfaces.mjs')) {
  throw new Error('README does not document deterministic screenshot regeneration');
}

console.log('screenshot capture contract ok');
