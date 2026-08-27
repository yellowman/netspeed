/* Optional presentation enhancements shared by all web interfaces. */
(function () {
    'use strict';

    const STAGES = [
        { key: 'meta', label: 'Handshake' },
        { key: 'latency', label: 'Idle latency' },
        { key: 'download', label: 'Download' },
        { key: 'upload', label: 'Upload' },
        { key: 'loaded-latency', label: 'Load response' },
        { key: 'packet-loss', label: 'Packet path' },
        { key: 'complete', label: 'Analysis' }
    ];
    const OUTCOMES = ['pending', 'running', 'succeeded', 'unavailable', 'failed'];
    const TERMINAL = new Set(['succeeded', 'unavailable', 'failed']);

    function textOf(element) {
        return element ? element.textContent.replace(/\s+/g, ' ').trim() : '';
    }

    function numericWidth(element) {
        if (!element) return 0;
        const inline = Number.parseFloat(element.style.width || '0');
        return Number.isFinite(inline) ? Math.max(0, Math.min(100, inline)) : 0;
    }

    function setupMirrors() {
        document.querySelectorAll('[data-mirror]').forEach(mirror => {
            const source = document.getElementById(mirror.dataset.mirror);
            if (!source) return;
            const update = () => {
                const value = textOf(source);
                if (value) mirror.textContent = value;
            };
            update();
            new MutationObserver(update).observe(source, {
                childList: true,
                characterData: true,
                subtree: true
            });
        });
    }

    function setupClock() {
        const clocks = Array.from(document.querySelectorAll('[data-live-clock]'));
        if (!clocks.length) return;
        const update = () => {
            const value = new Intl.DateTimeFormat(undefined, {
                hour: '2-digit',
                minute: '2-digit',
                second: '2-digit',
                hour12: false
            }).format(new Date());
            clocks.forEach(clock => { clock.textContent = value; });
        };
        update();
        window.setInterval(update, 1000);
    }

    function normalizedOutcome(value) {
        return OUTCOMES.includes(value) ? value : 'pending';
    }

    function setupSequence() {
        const body = document.body;
        const fill = document.getElementById('progressFill');
        const stageLabel = document.querySelector('[data-stage-label]');
        const percentLabel = document.querySelector('[data-progress-percent]');
        const items = Array.from(document.querySelectorAll('[data-progress-stage]'));
        if (!items.length) return;

        const state = Object.create(null);
        for (const definition of STAGES) {
            state[definition.key] = { stage: definition.key, outcome: 'pending' };
        }

        const appState = window.NetspeedApp?.state?.stageOutcomes;
        if (appState && typeof appState === 'object') {
            for (const [stage, change] of Object.entries(appState)) {
                if (state[stage] && change) state[stage] = { ...change };
            }
        }

        function updateProgress() {
            const running = STAGES.filter(definition => normalizedOutcome(state[definition.key]?.outcome) === 'running');
            const terminal = STAGES.filter(definition => TERMINAL.has(normalizedOutcome(state[definition.key]?.outcome)));
            const completeOutcome = normalizedOutcome(state.complete?.outcome);

            let progress = 0;
            if (completeOutcome === 'succeeded') {
                progress = 100;
            } else {
                let furthest = -1;
                const progressStages = terminal.filter(definition =>
                    definition.key !== 'complete' || completeOutcome === 'succeeded'
                );
                for (const definition of [...progressStages, ...running]) {
                    furthest = Math.max(furthest, STAGES.findIndex(item => item.key === definition.key));
                }
                if (furthest >= 0) {
                    const local = running.some(definition => definition.key === STAGES[furthest].key)
                        ? numericWidth(fill) / 100
                        : 1;
                    progress = ((furthest + Math.max(0.08, local)) / STAGES.length) * 100;
                }
            }
            if (percentLabel) percentLabel.textContent = `${Math.round(Math.min(100, progress))}%`;
        }

        function updateLabel() {
            const running = STAGES.filter(definition => normalizedOutcome(state[definition.key]?.outcome) === 'running');
            const failed = STAGES.filter(definition => normalizedOutcome(state[definition.key]?.outcome) === 'failed');
            const unavailable = STAGES.filter(definition => normalizedOutcome(state[definition.key]?.outcome) === 'unavailable');
            let label = 'Ready';
            if (running.length) {
                label = running[running.length - 1].label;
            } else if (normalizedOutcome(state.complete?.outcome) === 'succeeded') {
                label = 'Analysis complete';
            } else if (failed.length) {
                label = `${failed[failed.length - 1].label} failed`;
            } else if (unavailable.length) {
                label = `${unavailable[unavailable.length - 1].label} unavailable`;
            }
            if (stageLabel) stageLabel.textContent = label;
        }

        function render() {
            let bodyStage = 'ready';
            for (const definition of STAGES) {
                const outcome = normalizedOutcome(state[definition.key]?.outcome);
                if (outcome === 'running') bodyStage = definition.key;
            }
            if (normalizedOutcome(state.complete?.outcome) === 'succeeded') bodyStage = 'complete';
            body.dataset.testStage = bodyStage;

            for (const item of items) {
                const stage = item.dataset.progressStage;
                const change = state[stage] || { stage, outcome: 'pending' };
                const outcome = normalizedOutcome(change.outcome);
                item.dataset.outcome = outcome;
                item.classList.toggle('is-pending', outcome === 'pending');
                item.classList.toggle('is-active', outcome === 'running');
                item.classList.toggle('is-complete', outcome === 'succeeded');
                item.classList.toggle('is-unavailable', outcome === 'unavailable');
                item.classList.toggle('is-failed', outcome === 'failed');
                item.setAttribute('aria-label', `${STAGES.find(definition => definition.key === stage)?.label || stage}: ${outcome}`);
                if (change.reason) item.title = change.reason;
                else item.removeAttribute('title');
            }
            updateLabel();
            updateProgress();
        }

        document.addEventListener('netspeed:stagechange', event => {
            const change = event.detail;
            if (!change || !state[change.stage]) return;
            state[change.stage] = { ...change };
            render();
        });

        if (fill) {
            new MutationObserver(updateProgress).observe(fill, {
                attributes: true,
                attributeFilter: ['style']
            });
        }
        render();
    }

    function setupInterfaceLinks() {
        const selected = document.body.dataset.interface;
        const params = new URLSearchParams(window.location.search);
        const sharedResult = params.get('r');

        document.querySelectorAll('[data-interface-link]').forEach(link => {
            const current = link.dataset.interfaceLink === selected;
            link.classList.toggle('is-current', current);
            if (current) link.setAttribute('aria-current', 'page');
            else link.removeAttribute('aria-current');

            const target = new URL(link.getAttribute('href'), window.location.href);
            target.search = '';
            target.hash = '';
            if (sharedResult) target.searchParams.set('r', sharedResult);
            link.href = target.href;
        });
    }

    function init() {
        setupInterfaceLinks();
        setupMirrors();
        setupClock();
        setupSequence();
        document.documentElement.classList.add('interface-ready');
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init, { once: true });
    } else {
        init();
    }
})();
