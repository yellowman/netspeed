/* Optional presentation enhancements shared by alternate.html and phosphor.html. */
(function () {
    'use strict';

    const STAGES = [
        { key: 'meta', label: 'Handshake' },
        { key: 'latency', label: 'Idle latency' },
        { key: 'download', label: 'Download' },
        { key: 'upload', label: 'Upload' },
        { key: 'loaded-latency', label: 'Load response' },
        { key: 'packet-loss', label: 'Packet path' },
        { key: 'complete', label: 'Analysis complete' }
    ];

    const STAGE_BASE = {
        ready: 0,
        meta: 3,
        latency: 10,
        download: 25,
        upload: 50,
        'loaded-latency': 75,
        'packet-loss': 82,
        complete: 100
    };

    function textOf(element) {
        return element ? element.textContent.replace(/\s+/g, ' ').trim() : '';
    }

    function stageFromStatus(status) {
        const value = status.toLowerCase();
        if (value.includes('complete')) return 'complete';
        if (value.includes('packet')) return 'packet-loss';
        if (value.includes('loaded')) return 'loaded-latency';
        if (value.includes('upload')) return 'upload';
        if (value.includes('download') || value.includes('warm')) return 'download';
        if (value.includes('latency')) return 'latency';
        if (value.includes('metadata') || value.includes('handshake')) return 'meta';
        return 'ready';
    }

    function numericWidth(element) {
        if (!element) return 0;
        const inline = Number.parseFloat(element.style.width || '0');
        return Number.isFinite(inline) ? Math.max(0, Math.min(100, inline)) : 0;
    }

    function overallProgress(stage, localProgress) {
        const base = STAGE_BASE[stage] || 0;
        if (stage === 'download' || stage === 'upload') {
            return Math.min(base + (localProgress * 0.23), stage === 'download' ? 48 : 73);
        }
        if (stage === 'packet-loss') return Math.min(82 + (localProgress * 0.16), 98);
        return base;
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

    function setupSequence() {
        const body = document.body;
        const status = document.getElementById('progressStatus');
        const fill = document.getElementById('progressFill');
        const stageLabel = document.querySelector('[data-stage-label]');
        const percentLabel = document.querySelector('[data-progress-percent]');
        const items = Array.from(document.querySelectorAll('[data-progress-stage]'));
        if (!status || !items.length) return;

        const update = () => {
            const stage = stageFromStatus(textOf(status));
            const activeIndex = STAGES.findIndex(item => item.key === stage);
            body.dataset.testStage = stage;
            items.forEach((item, index) => {
                item.classList.toggle('is-active', index === activeIndex);
                item.classList.toggle('is-complete', index < activeIndex || (stage === 'complete' && index <= activeIndex));
                item.classList.toggle('is-pending', index > activeIndex);
                if (stage === 'ready') {
                    item.classList.remove('is-active', 'is-complete');
                    item.classList.add('is-pending');
                }
            });
            const definition = STAGES.find(item => item.key === stage);
            if (stageLabel) stageLabel.textContent = definition ? definition.label : 'Ready';
            if (percentLabel) percentLabel.textContent = `${Math.round(overallProgress(stage, numericWidth(fill)))}%`;
        };

        const observer = new MutationObserver(update);
        observer.observe(status, { childList: true, characterData: true, subtree: true });
        if (fill) observer.observe(fill, { attributes: true, attributeFilter: ['style'] });
        document.getElementById('startTestBtn')?.addEventListener('click', () => window.requestAnimationFrame(update));
        update();
    }

    function markCurrentInterface() {
        const selected = document.body.dataset.interface;
        document.querySelectorAll('[data-interface-link]').forEach(link => {
            const current = link.dataset.interfaceLink === selected;
            link.classList.toggle('is-current', current);
            if (current) link.setAttribute('aria-current', 'page');
        });
    }

    function init() {
        markCurrentInterface();
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
