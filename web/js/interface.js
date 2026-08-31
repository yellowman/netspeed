/* Presentation adapter shared by Standard, Observatory, and Phosphor.
 * Measurement state comes from structured events/data attributes; prose is never
 * treated as evidence that a stage succeeded.
 */
(() => {
  'use strict';

  const OUTCOMES = new Set(['pending', 'running', 'succeeded', 'unavailable', 'failed']);
  const TERMINAL_OUTCOMES = new Set(['succeeded', 'unavailable', 'failed']);
  const STAGE_ORDER = ['meta', 'latency', 'download', 'upload', 'loaded-latency', 'packet-loss', 'complete'];
  const STAGE_LABELS = {
    meta: 'Handshake',
    latency: 'Idle latency',
    download: 'Download',
    upload: 'Upload',
    'loaded-latency': 'Load response',
    'packet-loss': 'Packet path',
    complete: 'Analysis'
  };
  const aliases = {
    meta: 'meta', metadata: 'meta', handshake: 'meta', connect: 'meta',
    latency: 'latency', idle: 'latency', idlelatency: 'latency',
    download: 'download', down: 'download',
    upload: 'upload', up: 'upload',
    loaded: 'loaded-latency', loadedlatency: 'loaded-latency', loadedresponse: 'loaded-latency',
    packet: 'packet-loss', packetloss: 'packet-loss', packetpath: 'packet-loss',
    analysis: 'complete', complete: 'complete', results: 'complete'
  };
  const stageState = new Map();
  const receivedEvents = new WeakSet();
  let liveClockTimer = null;

  const nodesFor = stage => Array.from(document.querySelectorAll(
    `[data-stage="${CSS.escape(stage)}"], [data-progress-stage="${CSS.escape(stage)}"]`
  ));

  function normalizeStage(value) {
    if (!value) return '';
    const raw = String(value).trim();
    const key = raw.toLowerCase().replace(/[^a-z0-9]+/g, '');
    return aliases[key] || raw;
  }

  function normalizeOutcome(value) {
    const raw = String(value || '').toLowerCase();
    if (raw === 'complete' || raw === 'completed' || raw === 'success' || raw === 'ok') return 'succeeded';
    if (raw === 'skipped' || raw === 'unsupported' || raw === 'n/a') return 'unavailable';
    if (raw === 'error') return 'failed';
    return OUTCOMES.has(raw) ? raw : 'pending';
  }

  function stageLabel(stage) {
    const node = nodesFor(stage)[0];
    return node?.dataset.label || node?.querySelector('b')?.textContent || STAGE_LABELS[stage] || stage;
  }

  function updateSequenceSummary() {
    const outcomes = STAGE_ORDER.map(stage => stageState.get(stage)?.outcome || 'pending');
    const terminalCount = outcomes.filter(outcome => TERMINAL_OUTCOMES.has(outcome)).length;
    const runningIndex = outcomes.indexOf('running');
    const completeOutcome = stageState.get('complete')?.outcome;
    const failedIndex = outcomes.indexOf('failed');

    let label = 'Ready';
    let currentStage = '';
    let currentOutcome = 'pending';
    if (runningIndex >= 0) {
      currentStage = STAGE_ORDER[runningIndex];
      currentOutcome = 'running';
      label = stageLabel(currentStage);
    } else if (completeOutcome === 'succeeded') {
      currentStage = 'complete';
      currentOutcome = 'succeeded';
      label = 'Test complete';
    } else if (failedIndex >= 0) {
      currentStage = STAGE_ORDER[failedIndex];
      currentOutcome = 'failed';
      label = `${stageLabel(currentStage)} failed`;
    } else if (terminalCount > 0) {
      const lastTerminal = outcomes.reduce((last, outcome, index) =>
        TERMINAL_OUTCOMES.has(outcome) ? index : last, -1);
      currentStage = lastTerminal >= 0 ? STAGE_ORDER[lastTerminal] : '';
      currentOutcome = lastTerminal >= 0 ? outcomes[lastTerminal] : 'pending';
      label = currentStage ? `${stageLabel(currentStage)} complete` : 'Ready';
    }

    const percent = Math.round((terminalCount / STAGE_ORDER.length) * 100);
    for (const node of document.querySelectorAll('[data-stage-label]')) node.textContent = label;
    for (const node of document.querySelectorAll('[data-progress-percent]')) node.textContent = `${percent}%`;

    const root = document.documentElement;
    if (root) {
      root.dataset.measurementStage = currentStage;
      root.dataset.measurementOutcome = currentOutcome;
      root.style.setProperty('--measurement-progress', `${percent}%`);
    }
  }

  function setStageOutcome(stageValue, outcomeValue, detail = '') {
    const stage = normalizeStage(stageValue);
    if (!stage) return;
    const outcome = normalizeOutcome(outcomeValue);
    stageState.set(stage, { outcome, detail: String(detail || '') });

    for (const node of nodesFor(stage)) {
      node.dataset.outcome = outcome;
      node.classList.remove('is-pending', 'is-running', 'is-complete', 'is-succeeded', 'is-unavailable', 'is-failed', 'is-active');
      node.classList.add(`is-${outcome}`);
      if (outcome === 'running') node.classList.add('is-active');
      if (outcome === 'succeeded') node.classList.add('is-complete');
      const label = node.dataset.label || node.querySelector('b')?.textContent || STAGE_LABELS[stage] || stage;
      node.setAttribute('aria-label', `${label}: ${outcome}${detail ? ` — ${detail}` : ''}`);
      node.title = detail || outcome;
      const stateNode = node.querySelector('[data-stage-state]');
      if (stateNode) stateNode.textContent = outcome.toUpperCase();
      const detailNode = node.querySelector('[data-stage-detail]');
      if (detailNode && detail) detailNode.textContent = detail;
    }

    updateSequenceSummary();
    document.dispatchEvent(new CustomEvent('netspeed:presentation-stage', { detail: { stage, outcome, detail } }));
  }

  function receiveStageEvent(event) {
    if (event && typeof event === 'object') {
      if (receivedEvents.has(event)) return;
      receivedEvents.add(event);
    }
    const d = event && event.detail;
    if (!d) return;
    if (Array.isArray(d)) {
      d.forEach(value => value && setStageOutcome(
        value.stage || value.name,
        value.outcome || value.state,
        value.detail || value.message
      ));
      return;
    }
    setStageOutcome(
      d.stage || d.name || d.id,
      d.outcome || d.state || d.status,
      d.detail || d.message || d.reason
    );
  }

  function resetStages() {
    stageState.clear();
    for (const stage of STAGE_ORDER) setStageOutcome(stage, 'pending', '');
  }

  function preserveSharedResultLinks() {
    const params = new URLSearchParams(location.search);
    const result = params.get('r');
    document.querySelectorAll('a[href]').forEach(link => {
      let target;
      try { target = new URL(link.getAttribute('href'), location.href); } catch (_) { return; }
      if (!/(^|\/)(index|alternate|phosphor)\.html$/.test(target.pathname)) return;
      target.search = '';
      target.hash = '';
      if (result) target.searchParams.set('r', result);
      link.href = target.href;
    });
  }

  function bindMirrors() {
    document.querySelectorAll('[data-mirror], [data-observe], [data-observation], [data-source-id], [data-value-source], [data-copy-from]').forEach(target => {
      const id = target.dataset.mirror || target.dataset.observe || target.dataset.observation || target.dataset.sourceId || target.dataset.valueSource || target.dataset.copyFrom;
      const source = id && document.getElementById(id);
      if (!source) return;
      const copy = () => {
        const raw = ('value' in source && source.value !== undefined && source.value !== '') ? source.value : source.textContent;
        const prefix = target.dataset.prefix || '';
        const suffix = target.dataset.suffix || '';
        target.textContent = `${prefix}${raw == null ? '' : raw}${suffix}`;
        if (target instanceof HTMLProgressElement || target instanceof HTMLMeterElement) {
          const numeric = Number.parseFloat(raw);
          if (Number.isFinite(numeric)) target.value = numeric;
        }
        ['data-value', 'data-state', 'aria-label'].forEach(name => {
          if (source.hasAttribute(name)) target.setAttribute(name, source.getAttribute(name));
        });
      };
      copy();
      new MutationObserver(copy).observe(source, { childList: true, subtree: true, characterData: true, attributes: true });
      source.addEventListener('input', copy);
      source.addEventListener('change', copy);
    });
  }

  function bindDeclarativeStageState() {
    document.querySelectorAll('[data-stage-outcome]').forEach(source => {
      const apply = () => setStageOutcome(
        source.dataset.stage || source.dataset.progressStage,
        source.dataset.stageOutcome,
        source.dataset.stageDetail || ''
      );
      apply();
      new MutationObserver(apply).observe(source, {
        attributes: true,
        attributeFilter: ['data-stage-outcome', 'data-stage-detail']
      });
    });
  }

  function formatClock(date) {
    return [date.getHours(), date.getMinutes(), date.getSeconds()]
      .map(value => String(value).padStart(2, '0'))
      .join(':');
  }

  function bindLiveClocks() {
    const clocks = Array.from(document.querySelectorAll('[data-live-clock]'));
    if (clocks.length === 0) return;

    const update = () => {
      const value = formatClock(new Date());
      for (const clock of clocks) {
        clock.textContent = value;
        clock.setAttribute('datetime', value);
      }
    };

    update();
    if (liveClockTimer === null && typeof window.setInterval === 'function') {
      liveClockTimer = window.setInterval(update, 1000);
    }
  }

  function boot() {
    preserveSharedResultLinks();
    bindMirrors();
    bindLiveClocks();
    resetStages();
    bindDeclarativeStageState();
    document.documentElement.classList.add('presentation-ready');
  }

  const eventNames = [
    'netspeed-stage',
    'netspeed:measurement-stage',
    'netspeed:stage',
    'netspeed:stage-outcome',
    'netspeed:stagechange'
  ];
  for (const name of eventNames) {
    window.addEventListener(name, receiveStageEvent);
    document.addEventListener(name, receiveStageEvent);
  }

  window.NetspeedInterface = Object.freeze({
    setStageOutcome,
    resetStages,
    outcomes: stageState,
    stages: Object.freeze([...STAGE_ORDER])
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot, { once: true });
  } else {
    boot();
  }
})();
