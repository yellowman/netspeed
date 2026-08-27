/* Presentation adapter shared by Standard, Observatory, and Phosphor.
 * Measurement state comes from structured events/data attributes; prose is never
 * treated as evidence that a stage succeeded.
 */
(() => {
  'use strict';
  const OUTCOMES = new Set(['pending', 'running', 'succeeded', 'unavailable', 'failed']);
  const aliases = {
    handshake: 'handshake', metadata: 'handshake', connect: 'handshake',
    latency: 'latency', idle: 'latency', idleLatency: 'latency',
    download: 'download', down: 'download',
    upload: 'upload', up: 'upload',
    loaded: 'loaded', loadedLatency: 'loaded', loadedResponse: 'loaded',
    packet: 'packet', packetLoss: 'packet', packetPath: 'packet',
    analysis: 'analysis', complete: 'analysis', results: 'analysis'
  };
  const stageState = new Map();
  const nodesFor = stage => Array.from(document.querySelectorAll(
    `[data-stage="${CSS.escape(stage)}"], [data-progress-stage="${CSS.escape(stage)}"]`
  ));
  function normalizeStage(value) {
    if (!value) return '';
    const raw = String(value).trim();
    return aliases[raw] || aliases[raw.replace(/[ _-]+(.)/g, (_, c) => c.toUpperCase())] || raw;
  }
  function normalizeOutcome(value) {
    const raw = String(value || '').toLowerCase();
    if (raw === 'complete' || raw === 'completed' || raw === 'success' || raw === 'ok') return 'succeeded';
    if (raw === 'skipped' || raw === 'unsupported' || raw === 'n/a') return 'unavailable';
    if (raw === 'error') return 'failed';
    return OUTCOMES.has(raw) ? raw : 'pending';
  }
  function setStageOutcome(stageValue, outcomeValue, detail = '') {
    const stage = normalizeStage(stageValue);
    if (!stage) return;
    const outcome = normalizeOutcome(outcomeValue);
    stageState.set(stage, { outcome, detail: String(detail || '') });
    for (const node of nodesFor(stage)) {
      node.dataset.outcome = outcome;
      node.classList.remove('is-pending','is-running','is-complete','is-succeeded','is-unavailable','is-failed','is-active');
      node.classList.add(`is-${outcome}`);
      if (outcome === 'running') node.classList.add('is-active');
      if (outcome === 'succeeded') node.classList.add('is-complete');
      const label = node.dataset.label || node.querySelector('[data-stage-label]')?.textContent || stage;
      node.setAttribute('aria-label', `${label}: ${outcome}${detail ? ` — ${detail}` : ''}`);
      node.title = detail || outcome;
      const stateNode = node.querySelector('[data-stage-state]');
      if (stateNode) stateNode.textContent = outcome.toUpperCase();
      const detailNode = node.querySelector('[data-stage-detail]');
      if (detailNode && detail) detailNode.textContent = detail;
    }
    document.dispatchEvent(new CustomEvent('netspeed:presentation-stage', { detail: { stage, outcome, detail } }));
  }
  function receiveStageEvent(event) {
    const d = event && event.detail;
    if (!d) return;
    if (Array.isArray(d)) { d.forEach(x => x && setStageOutcome(x.stage || x.name, x.outcome || x.state, x.detail || x.message)); return; }
    setStageOutcome(d.stage || d.name || d.id, d.outcome || d.state || d.status, d.detail || d.message || d.reason);
  }
  function resetStages() {
    document.querySelectorAll('[data-stage], [data-progress-stage]').forEach(node => {
      const stage = node.dataset.stage || node.dataset.progressStage;
      if (stage) setStageOutcome(stage, 'pending', '');
    });
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
        ['data-value','data-state','aria-label'].forEach(name => { if (source.hasAttribute(name)) target.setAttribute(name, source.getAttribute(name)); });
      };
      copy();
      new MutationObserver(copy).observe(source, { childList:true, subtree:true, characterData:true, attributes:true });
      source.addEventListener('input', copy);
      source.addEventListener('change', copy);
    });
  }
  function bindDeclarativeStageState() {
    document.querySelectorAll('[data-stage-outcome]').forEach(source => {
      const apply = () => setStageOutcome(source.dataset.stage || source.dataset.progressStage, source.dataset.stageOutcome, source.dataset.stageDetail || '');
      apply();
      new MutationObserver(apply).observe(source, { attributes:true, attributeFilter:['data-stage-outcome','data-stage-detail'] });
    });
  }
  function boot() {
    preserveSharedResultLinks();
    bindMirrors();
    resetStages();
    bindDeclarativeStageState();
    document.documentElement.classList.add('presentation-ready');
  }
  window.addEventListener("netspeed-stage", receiveStageEvent);
  window.addEventListener("netspeed:measurement-stage", receiveStageEvent);
  window.addEventListener("netspeed:stage", receiveStageEvent);
  window.addEventListener("netspeed:stage-outcome", receiveStageEvent);
  window.addEventListener("netspeed:stagechange", receiveStageEvent);
  window.NetspeedInterface = Object.freeze({ setStageOutcome, resetStages, outcomes: stageState });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot, { once:true }); else boot();
})();
