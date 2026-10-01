/* Evidence is a lossless snapshot, never reconstructed from display prose. */
(function (root) {
    'use strict';
    const names = { overview: 'Overview', throughput: 'Throughput', latency: 'Latency', packet: 'Packet delivery', transport: 'Transport & verification', raw: 'Raw evidence' };
    let result = null;
    let inspection = { section: 'overview' };
    const number = value => Number.isFinite(value) ? value.toFixed(1) : 'Not measured';
    const printable = value => value == null ? 'Not available' : typeof value === 'object' ? JSON.stringify(value) : String(value);
    const snapshot = value => value ? JSON.parse(JSON.stringify(value)) : null;

    function latencyEvidence(data, condition) {
        const all = (data?.latencySamples || []).filter(sample => sample.condition === condition);
        const accepted = all.filter(sample => Number.isFinite(sample.rttMs) && sample.rttMs > 0 && !sample.timingResolutionLimited && (condition === 'unloaded' || sample.loadOverlapped === true));
        const sorted = accepted.map(sample => sample.rttMs).sort((a, b) => a - b);
        const mid = Math.floor(sorted.length / 2);
        const rank = (sorted.length - 1) * .9;
        const lower = Math.floor(rank), upper = Math.ceil(rank);
        return {
            accepted: accepted.length, observed: all.length,
            min: sorted[0] ?? null,
            median: sorted.length ? sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2 : null,
            p90: sorted.length ? sorted[lower] + (sorted[upper] - sorted[lower]) * (rank - lower) : null,
            jitter: sorted.length ? sorted[lower] + (sorted[upper] - sorted[lower]) * (rank - lower) - (sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2) : null,
            max: sorted.at(-1) ?? null,
            belowResolution: all.filter(sample => sample.timingResolutionLimited).length,
            discarded: (data?.discardedLatencySamples || []).filter(sample => sample.condition === condition).length,
            connectionReused: accepted.filter(sample => sample.connectionReused === true).length,
            overlapped: condition === 'unloaded' ? null : accepted.length,
            transport: [...new Set(accepted.map(sample => sample.probeTransport).filter(Boolean))].join(', ') || data?.httpTransport?.latency?.probeTransport || 'Not available'
        };
    }

    function measurementNotes(data) {
        if (!data) return [];
        const notes = [...(data.testConfidence?.warnings || []).map(String)];
        const limited = (data.latencySamples || []).filter(sample => sample.timingResolutionLimited).length || data.httpTransport?.latency?.webSocket?.timingResolutionLimitedMessages || 0;
        if (limited) notes.push(`Browser timer resolution limited ${limited} latency sample${limited === 1 ? '' : 's'}; excluded from RTT statistics.`);
        if (data.packetLoss?.unavailable) notes.push(`Packet delivery not measured: ${data.packetLoss.reason || 'packet path unavailable'}.`);
        if (data.dataChannelStats?.connectionType === 'relay') notes.push('Packet delivery used a TURN relay rather than a direct path.');
        if (data.httpTransport?.latency?.fallbackUsed) notes.push(`Latency used HTTP fallback: ${data.httpTransport.latency.fallbackReason || 'WebSocket unavailable'}.`);
        if (data.error) notes.push(`Test incomplete: ${data.error.message || data.error}.`);
        if (data.sharedResult) notes.push('Shared links contain a compact subset; full receipts require the original JSON export.');
        return [...new Set(notes)];
    }

    function fields(data, prefix = '') {
        if (!data || typeof data !== 'object') return [];
        return Object.entries(data).flatMap(([key, value]) => {
            const label = key.replace(/([a-z])([A-Z])/g, '$1 $2');
            const path = prefix ? `${prefix} / ${label}` : label;
            return value && typeof value === 'object' && !Array.isArray(value) ? fields(value, path) : [[path, printable(value)]];
        });
    }

    const node = id => root.document.getElementById(id);
    function fillFields(target, entries) {
        target.replaceChildren();
        const list = root.document.createElement('dl'); list.className = 'evidence-table';
        for (const [key, value] of entries) {
            const row = root.document.createElement('div');
            const term = root.document.createElement('dt'); term.textContent = key;
            const description = root.document.createElement('dd'); description.textContent = printable(value);
            row.append(term, description); list.append(row);
        }
        if (!entries.length) list.textContent = 'Not measured. No evidence recorded.';
        target.append(list);
    }

    function table(target, samples, columns) {
        target.replaceChildren();
        if (!samples.length) { target.textContent = 'No samples recorded.'; return; }
        const wrapper = root.document.createElement('div'); wrapper.className = 'evidence-table-scroll';
        const element = root.document.createElement('table');
        const head = root.document.createElement('thead'); const headings = root.document.createElement('tr');
        for (const [title] of columns) { const cell = root.document.createElement('th'); cell.scope = 'col'; cell.textContent = title; headings.append(cell); }
        head.append(headings); element.append(head);
        const body = root.document.createElement('tbody');
        samples.forEach((sample, index) => {
            const row = root.document.createElement('tr');
            for (const [, read] of columns) { const cell = root.document.createElement('td'); cell.textContent = printable(read(sample, index)); row.append(cell); }
            body.append(row);
        });
        element.append(body); wrapper.append(element); target.append(wrapper);
    }

    function select(section, focus = false) {
        if (!names[section]) return;
        root.document.querySelectorAll('[data-evidence-tab]').forEach(tab => {
            const active = tab.dataset.evidenceTab === section;
            tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1;
            node(`panel-${tab.dataset.evidenceTab}`).hidden = !active;
            if (active && focus) tab.focus();
        });
        if (node('consoleSection')) node('consoleSection').value = section;
    }

    function inspect(section, options = {}) {
        inspection = { section, ...options };
        const direction = options.direction, condition = options.condition;
        node('inspectorTitle').textContent = `${direction || condition || names[section]} / evidence`;
        let entries = [];
        if (result) {
            if (section === 'throughput') {
                const samples = (result.throughputSamples || []).filter(sample => !direction || sample.direction === direction);
                const last = samples.at(-1);
                const windows = samples.filter(sample => sample.sampleKind === 'window');
                entries = [
                    ['Sustained Mbps', direction ? result.summary?.[`${direction}Mbps`] : null],
                    ['Samples / windows', `${samples.length} / ${windows.length}`],
                    ['Window payload bytes', windows.reduce((sum, sample) => sum + (sample.sizeBytes ?? sample.bytes ?? 0), 0)],
                    ['Last window ms', last?.durationMs], ['Workers', last?.concurrency], ['Chunk bytes', last?.chunkBytes]
                ].concat(fields(result.httpTransport?.selection), fields(result.httpTransport?.responseVerifications));
            } else if (section === 'latency') entries = fields(latencyEvidence(result, condition || 'unloaded')).concat(fields(result.httpTransport?.latency));
            else if (section === 'packet') entries = fields(result.packetLoss).concat(fields(result.dataChannelStats));
            else if (section === 'transport') entries = fields(result.httpTransport?.selection).concat(fields(result.httpTransport?.requestControls));
            else entries = [
                ['Protocol version', result.meta?.measurementProtocolVersion],
                ['Started', result.startTime == null ? null : new Date(result.startTime).toISOString()],
                ['Duration ms', result.endTime != null && result.startTime != null ? result.endTime - result.startTime : null],
                ['Confidence', result.testConfidence?.overall], ['Quality', result.networkQualityScore?.description]
            ];
        }
        fillFields(node('inspectorContent'), entries);
        node('inspectorDetailsBtn').dataset.openEvidence = section;
    }

    function open(section, options = {}) {
        node('detailsWorkspace').open = true;
        select(section, true); inspect(section, options);
        node('detailsWorkspace').scrollIntoView({ behavior: 'auto', block: 'start' });
    }

    function render() {
        const data = result;
        node('rawEvidence').textContent = data ? JSON.stringify(data, null, 2) : 'No measurement yet.';
        for (const id of ['copyEvidenceBtn', 'downloadEvidenceBtn']) node(id).disabled = !data;
        const notes = measurementNotes(data), strip = node('measurementNotes'); strip.replaceChildren(); strip.hidden = notes.length === 0;
        if (notes.length) {
            const title = root.document.createElement('button'); title.className = 'text-button'; title.textContent = `${notes.length} measurement note${notes.length === 1 ? '' : 's'}`;
            title.addEventListener('click', () => open('transport')); strip.append(title);
            const list = root.document.createElement('ul');
            for (const note of notes) { const item = root.document.createElement('li'); item.textContent = note; list.append(item); }
            strip.append(list);
        }
        fillFields(node('overviewEvidence'), data ? fields({ testTime: data.startTime ? new Date(data.startTime).toISOString() : null, durationMs: data.endTime && data.startTime ? data.endTime - data.startTime : null, sharedResult: data.sharedResult === true, ...data.meta }) : []);
        for (const direction of ['download', 'upload']) {
            const samples = (data?.throughputSamples || []).filter(sample => sample.direction === direction);
            table(node(`${direction}WindowEvidence`), samples, [['#', (_, i) => i + 1], ['Kind', s => s.sampleKind || s.profile], ['Bytes', s => s.sizeBytes ?? s.bytes], ['Duration ms', s => number(s.durationMs)], ['Mbps', s => number(s.mbps)], ['Workers', s => s.concurrency], ['Requests', s => s.requestCount]]);
            node(`${direction}EvidenceLine`).textContent = samples.length ? `${samples.filter(s => s.sampleKind === 'window').length} windows · ${samples.length} samples · View evidence` : 'View transfer evidence';
        }
        for (const condition of ['unloaded', 'download', 'upload']) {
            const evidence = latencyEvidence(data, condition);
            const summaryKey = { unloaded: 'latencyUnloadedMs', download: 'latencyDownloadMs', upload: 'latencyUploadMs' }[condition];
            fillFields(node(`${condition}LatencyEvidence`), [
                ['Summary ms', data?.summary?.[summaryKey]],
                ['Summary statistic', condition === 'unloaded' ? 'R-7 median; first 2 valid probes excluded, then IQR filter' : 'R-7 p90; overlap required, then IQR filter'],
                ['Distribution', 'All resolved, overlap-verified probes; no summary trimming']
            ].concat(fields(evidence), fields(data?.httpTransport?.latency ? { sessionWarmups: data.httpTransport.latency.warmupRequests, protocol: data.httpTransport.latency.webSocket?.protocol, fallback: data.httpTransport.latency.fallbackUsed ? data.httpTransport.latency.fallbackReason : 'Not used' } : null)));
            const samples = (data?.latencySamples || []).filter(s => s.condition === condition).concat((data?.discardedLatencySamples || []).filter(s => s.condition === condition));
            table(node(`${condition}SampleEvidence`), samples, [['#', (_, i) => i + 1], ['RTT ms', s => s.timingResolutionLimited ? 'Below resolution' : number(s.rttMs)], ['Transport', s => s.probeTransport], ['Reuse', s => s.connectionReused], ['Overlap', s => s.loadOverlapped], ['Discard reason', s => s.discardReason || '—']]);
            node(`${condition}LatencyEvidenceLine`).textContent = data ? `${evidence.accepted}/${evidence.observed} accepted · ${evidence.transport} · ${evidence.connectionReused} reused` : 'View sample evidence';
            const displayedMedian = condition === 'unloaded' && Number.isFinite(data?.summary?.latencyUnloadedMs) ? data.summary.latencyUnloadedMs : evidence.median;
            if (displayedMedian !== null) node(`${condition}LatencySummary`).textContent = `${number(displayedMedian)} ms`;
            const idle = Number.isFinite(data?.summary?.latencyUnloadedMs) ? data.summary.latencyUnloadedMs : latencyEvidence(data, 'unloaded').median;
            const delta = evidence.median !== null && idle !== null ? evidence.median - idle : null;
            node(`${condition}LatencyDelta`).textContent = condition !== 'unloaded' && delta !== null ? `${delta >= 0 ? '+' : ''}${number(delta)} ms vs idle` : '';
        }
        fillFields(node('packetEvidence'), fields(data?.packetLoss));
        fillFields(node('dataChannelEvidence'), fields(data?.dataChannelStats));
        fillFields(node('transportEvidence'), fields(data?.httpTransport).concat(fields(data?.meta?.measurementCapabilities, 'Server capabilities')));
        inspect(inspection.section, inspection);
    }

    function setResults(value) { result = snapshot(value); render(); }
    function boot() {
        const tabs = [...root.document.querySelectorAll('[data-evidence-tab]')];
        tabs.forEach((tab, index) => {
            tab.addEventListener('click', () => select(tab.dataset.evidenceTab));
            tab.addEventListener('keydown', event => {
                let next;
                if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
                else if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
                else if (event.key === 'Home') next = 0;
                else if (event.key === 'End') next = tabs.length - 1;
                else return;
                event.preventDefault(); select(tabs[next].dataset.evidenceTab, true);
            });
        });
        root.document.querySelectorAll('[data-open-evidence]').forEach(button => button.addEventListener('click', () => open(button.dataset.openEvidence, { direction: button.dataset.direction, condition: button.dataset.condition })));
        root.document.querySelectorAll('[data-inspect]').forEach(button => button.addEventListener('click', () => {
            const options = { direction: button.dataset.direction, condition: button.dataset.condition };
            if (root.document.body.dataset.interface === 'alternate') inspect(button.dataset.inspect, options);
            else open(button.dataset.inspect, options);
        }));
        node('consoleSection')?.addEventListener('change', event => select(event.target.value));
        const columns = node('terminalColumns');
        if (columns) {
            const saved = localStorage.getItem('netspeed-terminal-columns');
            const apply = value => {
                const valid = value === '132' ? '132' : '80';
                columns.value = valid; root.document.body.dataset.columns = valid;
                localStorage.setItem('netspeed-terminal-columns', valid);
                root.document.dispatchEvent(new CustomEvent('netspeed:terminal-width'));
            };
            apply(saved);
            columns.addEventListener('change', () => apply(columns.value));
            const back = () => { node('detailsWorkspace').open = false; node('startTestBtn').focus(); };
            node('terminalBack').addEventListener('click', back);
            root.document.addEventListener('keydown', event => {
                if (event.ctrlKey || event.altKey || event.metaKey || root.document.querySelector('.modal.active')) return;
                const shortcuts = { F1: 'overview', F2: 'throughput', F3: 'latency', F4: 'packet' };
                if (shortcuts[event.key]) { event.preventDefault(); open(shortcuts[event.key]); }
                else if (event.key === 'Escape') { event.preventDefault(); back(); }
            });
        }
        node('copyEvidenceBtn').addEventListener('click', () => root.document.dispatchEvent(new CustomEvent('netspeed:copy-evidence', { detail: { json: JSON.stringify(result, null, 2) } })));
        node('downloadEvidenceBtn').addEventListener('click', () => root.document.dispatchEvent(new CustomEvent('netspeed:download-evidence')));
        render();
    }
    const api = { snapshot, latencyEvidence, measurementNotes, fields, setResults, open, select, inspect, getResults: () => snapshot(result) };
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root.document) {
        root.NetspeedEvidence = api;
        if (root.document.readyState === 'loading') root.document.addEventListener('DOMContentLoaded', boot, { once: true });
        else boot();
    }
})(typeof window === 'object' ? window : globalThis);
