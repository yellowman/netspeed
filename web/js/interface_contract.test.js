#!/usr/bin/env node
'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.resolve(__dirname, '..');
const js = fs.readFileSync(path.join(__dirname, 'interface.js'), 'utf8');

if (/progressStatus[\s\S]{0,500}(complete|completed)/i.test(js)) {
  throw new Error('interface adapter infers evidence from status prose');
}
for (const page of ['index.html', 'alternate.html', 'phosphor.html']) {
  const html = fs.readFileSync(path.join(root, page), 'utf8');
  const count = (html.match(/js\/interface\.js/g) || []).length;
  if (count !== 1) throw new Error(`${page}: expected one interface.js, got ${count}`);
}
for (const outcome of ['pending', 'running', 'succeeded', 'unavailable', 'failed']) {
  if (!js.includes(`'${outcome}'`)) throw new Error(`missing structured outcome ${outcome}`);
}
if (!js.includes("params.get('r')")) throw new Error('shared-result preservation missing');
if (!js.includes("'loaded-latency': 'Load response'")) throw new Error('loaded-latency canonical stage missing');
if (!js.includes("'packet-loss': 'Packet path'")) throw new Error('packet-loss canonical stage missing');
if (!/document\.addEventListener\(name, receiveStageEvent\)/.test(js)) {
  throw new Error('document-dispatched measurement stages are not observed');
}
if (!/data-progress-percent/.test(js) || !/data-stage-label/.test(js)) {
  throw new Error('structured sequence summary is not maintained');
}

class FakeClassList {
  constructor() { this.values = new Set(); }
  add(...values) { values.forEach(value => this.values.add(value)); }
  remove(...values) { values.forEach(value => this.values.delete(value)); }
  contains(value) { return this.values.has(value); }
}

class FakeEventTarget {
  constructor() { this.listeners = new Map(); }
  addEventListener(name, listener) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }
  dispatchEvent(event) {
    for (const listener of this.listeners.get(event.type) || []) listener.call(this, event);
    return true;
  }
}

class FakeCustomEvent {
  constructor(type, options = {}) {
    this.type = type;
    this.detail = options.detail;
  }
}

function makeStageNode(stage, label) {
  const attributes = new Map();
  return {
    dataset: { progressStage: stage },
    classList: new FakeClassList(),
    title: '',
    querySelector(selector) {
      if (selector === 'b') return { textContent: label };
      return null;
    },
    setAttribute(name, value) { attributes.set(name, String(value)); },
    getAttribute(name) { return attributes.get(name) || null; }
  };
}

{
  const stageLabels = {
    meta: 'Handshake', latency: 'Idle latency', download: 'Download', upload: 'Upload',
    'loaded-latency': 'Load response', 'packet-loss': 'Packet path', complete: 'Analysis'
  };
  const stageNodes = new Map(Object.entries(stageLabels).map(([stage, label]) => [stage, makeStageNode(stage, label)]));
  const summaryLabel = { textContent: '' };
  const summaryPercent = { textContent: '' };
  const liveClockAttributes = new Map();
  const liveClock = {
    textContent: '',
    setAttribute(name, value) { liveClockAttributes.set(name, String(value)); }
  };
  const rootElement = {
    dataset: {},
    classList: new FakeClassList(),
    style: { properties: new Map(), setProperty(name, value) { this.properties.set(name, value); } }
  };
  const document = new FakeEventTarget();
  document.readyState = 'complete';
  document.documentElement = rootElement;
  document.querySelectorAll = selector => {
    if (selector === '[data-stage-label]') return [summaryLabel];
    if (selector === '[data-progress-percent]') return [summaryPercent];
    if (selector === '[data-live-clock]') return [liveClock];
    if (selector === '[data-stage], [data-progress-stage]') return [...stageNodes.values()];
    if (selector === 'a[href]' || selector.startsWith('[data-mirror]') || selector === '[data-stage-outcome]') return [];
    const matches = [...selector.matchAll(/data-(?:progress-)?stage="([^"]+)"/g)].map(match => match[1]);
    return matches.map(stage => stageNodes.get(stage)).filter(Boolean);
  };
  document.getElementById = () => null;

  const window = new FakeEventTarget();
  let intervalDelay = null;
  window.setInterval = (_callback, delay) => {
    intervalDelay = delay;
    return 1;
  };
  class FixedDate extends Date {
    constructor(...args) {
      super(...(args.length ? args : [2026, 7, 28, 20, 41, 32]));
    }
  }
  const context = {
    window,
    document,
    location: { search: '', href: 'http://example.test/alternate.html' },
    URL,
    URLSearchParams,
    CSS: { escape: value => String(value) },
    CustomEvent: FakeCustomEvent,
    WeakSet,
    Map,
    Set,
    Object,
    Array,
    String,
    Number,
    Math,
    Date: FixedDate,
    MutationObserver: class { observe() {} },
    HTMLProgressElement: class {},
    HTMLMeterElement: class {}
  };
  vm.runInNewContext(js, context, { filename: 'interface.js' });

  assert.equal(summaryLabel.textContent, 'Ready');
  assert.equal(summaryPercent.textContent, '0%');
  assert.equal(liveClock.textContent, '20:41:32');
  assert.equal(liveClockAttributes.get('datetime'), '20:41:32');
  assert.equal(intervalDelay, 1000);

  document.dispatchEvent(new FakeCustomEvent('netspeed:stagechange', {
    detail: { stage: 'loaded-latency', outcome: 'succeeded' }
  }));
  assert.equal(stageNodes.get('loaded-latency').dataset.outcome, 'succeeded');
  assert.equal(stageNodes.get('loaded-latency').classList.contains('is-complete'), true);
  assert.equal(summaryPercent.textContent, '14%');

  window.dispatchEvent(new FakeCustomEvent('netspeed:stagechange', {
    detail: { stage: 'packet path', outcome: 'success' }
  }));
  assert.equal(stageNodes.get('packet-loss').dataset.outcome, 'succeeded');
  assert.equal(summaryPercent.textContent, '29%');

  for (const stage of ['meta', 'latency', 'download', 'upload', 'complete']) {
    document.dispatchEvent(new FakeCustomEvent('netspeed:stagechange', {
      detail: { stage, outcome: 'succeeded' }
    }));
  }
  assert.equal(summaryLabel.textContent, 'Test complete');
  assert.equal(summaryPercent.textContent, '100%');
  assert.equal(rootElement.dataset.measurementStage, 'complete');
  assert.equal(rootElement.dataset.measurementOutcome, 'succeeded');
  assert.equal(rootElement.style.properties.get('--measurement-progress'), '100%');
}

console.log('interface contract ok');
