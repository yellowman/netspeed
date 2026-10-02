/* Evidence is a lossless snapshot, never reconstructed from display prose. */
(function (root) {
    'use strict';
    const names = { overview: 'Overview', throughput: 'Throughput', latency: 'Latency', packet: 'Packet delivery', transport: 'Transport & verification', raw: 'Raw evidence' };
    let result = null;
    let liveEvents = [];
    let inspection = root.document?.body?.dataset.interface === 'alternate'
        ? { section: 'throughput', direction: 'download' } : { section: 'overview' };
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

    function windowSamples(data) {
        return (data?.throughputSamples || []).filter(sample => sample.sampleKind === 'window');
    }

    function throughputEvidence(data, direction) {
        const samples = (data?.throughputSamples || []).filter(sample => !direction || sample.direction === direction);
        const windows = samples.filter(sample => sample.sampleKind === 'window');
        const lastWindow = windows.at(-1);
        const bytes = windows.map(sample => sample.sizeBytes ?? sample.bytes);
        const payloadBytes = bytes.length && bytes.every(value => Number.isFinite(value) && value >= 0)
            ? bytes.reduce((sum, value) => sum + value, 0) : null;
        return [
            ['Sustained Mbps', direction ? data?.summary?.[`${direction}Mbps`] : null],
            ['Observed samples', samples.length], ['Load windows', windows.length],
            ['Payload bytes across windows', payloadBytes],
            ['Last window ms', lastWindow?.durationMs], ['Workers', lastWindow?.concurrency],
            ['Chunk bytes', lastWindow?.chunkBytes]
        ].concat(
            fields(data?.httpTransport?.selection).map(([key, value]) => [key.charAt(0).toUpperCase() + key.slice(1).toLowerCase(), value]),
            fields(data?.httpTransport?.responseVerifications, 'Responses checked')
        );
    }

    function measurementSignature(data) {
        if (!data || data.sharedResult) return [];
        const parts = [], latency = data.httpTransport?.latency;
        if (latency?.probeTransport) {
            const transport = latency.probeTransport === 'websocket' ? 'WS' : latency.probeTransport.toUpperCase();
            parts.push(`${transport}${latency.verifiedReusedSamples > 0 ? ' reused' : ''}${latency.fallbackUsed ? ' / fallback used' : ''}`);
        }
        for (const protocol of latency?.nextHopProtocols || []) parts.push(`${protocol.toUpperCase()} (RTT)`);
        const selection = data.httpTransport?.selection;
        if (selection?.downloadPayload && selection?.downloadFraming) parts.push(`${selection.downloadPayload}/${selection.downloadFraming} selected`);
        for (const direction of ['download', 'upload']) {
            const counts = [...new Set(windowSamples(data).filter(s => s.direction === direction).map(s => s.concurrency).filter(Number.isFinite))];
            if (counts.length) parts.push(`${direction === 'download' ? 'DL' : 'UL'} ${counts.join('/')} flows`);
        }
        if (Number.isFinite(data.startTime) && Number.isFinite(data.endTime) && data.endTime >= data.startTime) parts.push(`${((data.endTime - data.startTime) / 1000).toFixed(1)} s`);
        return parts;
    }

    function connectionBranches(data) {
        if (!data) return [];
        const branches = [], server = data.meta?.serverName || data.server || 'Measurement node';
        if ((data.throughputSamples || []).length) branches.push({ from: 'Client', link: 'HTTP throughput', to: server });
        const latency = data.httpTransport?.latency;
        if (latency?.probeTransport) branches.push({ from: 'Client', link: `${latency.probeTransport === 'websocket' ? 'WebSocket' : 'HTTP'} RTT${latency.verifiedReusedSamples > 0 ? ' / reused' : ''}`, to: `${server}${Number.isFinite(data.summary?.latencyUnloadedMs) ? ` · ${data.summary.latencyUnloadedMs.toFixed(1)} ms` : ''}` });
        const packet = data.packetLoss;
        if (packet?.unavailable) branches.push({ from: 'Packet delivery', link: 'Not measured', to: packet.reason || 'Path unavailable' });
        else if (packet) {
            const type = data.dataChannelStats?.connectionType;
            const path = { relay: 'TURN relay', srflx: 'STUN NAT', host: 'Host candidate', prflx: 'Peer-reflexive candidate' }[type];
            branches.push({ from: 'Packet delivery', link: path || 'Topology not recorded', to: Number.isFinite(packet.lossPercent) ? `${packet.lossPercent.toFixed(2)}% loss` : 'Loss not measured' });
        }
        return branches;
    }

    function sampleStrip(sample, index, total) {
        const parts = [`${sample.sampleKind === 'window' ? 'window' : 'sample'} ${index + 1}/${total}`];
        if (Number.isFinite(sample.mbps)) parts.push(`${sample.mbps.toFixed(1)} Mbps`);
        if (Number.isFinite(sample.sizeBytes ?? sample.bytes)) parts.push(`${((sample.sizeBytes ?? sample.bytes) / 1e6).toFixed(1)} MB`);
        if (Number.isFinite(sample.concurrency)) parts.push(`${sample.concurrency} flows`);
        if (Number.isFinite(sample.requestCount)) parts.push(`${sample.requestCount} req`);
        if (Number.isFinite(sample.durationMs)) parts.push(`${sample.durationMs.toFixed(0)} ms`);
        return parts.join(' · ');
    }

    function eventDescription(event) {
        const labels = { meta: 'Handshake', latency: 'Idle latency', download: 'Download', upload: 'Upload', 'loaded-latency': 'Load response', 'packet-loss': 'Packet path', complete: 'Analysis' };
        if (event.type === 'stage') return `${labels[event.stage] || event.stage} ${event.outcome}${event.reason ? ` · ${event.reason}` : ''}`;
        if (event.type === 'window') return `${event.direction === 'download' ? 'DL' : 'UL'} window ${Number.isInteger(event.windowIndex) ? event.windowIndex + 1 : ''} complete · ${sampleStrip({ ...event, sampleKind: 'window' }, 0, 1).split(' · ').slice(1).join(' · ')}`;
        if (event.type !== 'latency') return `Recorded event: ${event.type || 'unclassified'}`;
        const parts = [`${event.transport === 'websocket' ? 'WS echo' : event.transport === 'http' ? 'HTTP probe' : 'Latency probe'} ${event.timingResolutionLimited ? 'below resolution' : 'observed'}`];
        if (event.condition) parts.push(event.condition);
        if (event.connectionReused === true) parts.push('reused connection');
        if (Number.isFinite(event.rttMs) && !event.timingResolutionLimited) parts.push(`RTT ${event.rttMs.toFixed(1)} ms`);
        if (event.loadOverlapped === true) parts.push('load overlap verified');
        return parts.join(' · ');
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
        const title = direction ? { download: 'Download', upload: 'Upload' }[direction]
            : condition ? { unloaded: 'Unloaded latency', download: 'Latency during download', upload: 'Latency during upload' }[condition]
            : names[section];
        node('inspectorTitle').textContent = `${title || names[section]} / evidence`;
        let entries = [];
        if (result) {
            if (section === 'throughput') {
                entries = throughputEvidence(result, direction);
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
        node('inspectorDetailsBtn').dataset.direction = direction || '';
        node('inspectorDetailsBtn').dataset.condition = condition || '';
        root.document.querySelectorAll('[data-inspect]').forEach(button => {
            const selected = button.dataset.inspect === section
                && (!button.dataset.direction || button.dataset.direction === direction)
                && (!button.dataset.condition || button.dataset.condition === (condition || 'unloaded'));
            button.setAttribute('aria-pressed', String(selected));
        });
        root.document.querySelectorAll('[data-measurement]').forEach(figure => {
            figure.classList.toggle('is-inspected', root.document.body.dataset.interface === 'alternate'
                && figure.dataset.measurement === section
                && (section === 'throughput' ? figure.dataset.direction === direction : figure.dataset.condition === (condition || 'unloaded')));
        });
    }

    function renderConnection() {
        const signature = node('measurementSignature');
        if (signature) { signature.textContent = measurementSignature(result).join(' · '); signature.hidden = !signature.textContent; }
        const ribbon = node('connectionRibbon');
        if (!ribbon) return;
        const branches = connectionBranches(result); ribbon.replaceChildren(); ribbon.hidden = !branches.length;
        for (const branch of branches) {
            const row = root.document.createElement('div'); row.className = 'connection-branch';
            for (const [index, text] of [branch.from, branch.link, branch.to].entries()) {
                if (index) { const rule = root.document.createElement('span'); rule.className = 'ribbon-rule'; rule.setAttribute('aria-hidden', 'true'); row.append(rule); }
                const label = root.document.createElement('span'); label.className = index === 1 ? 'ribbon-link' : 'ribbon-node'; label.textContent = text; row.append(label);
            }
            ribbon.append(row);
        }
    }

    function renderEvents() {
        const events = Array.isArray(result?.measurementEvents) ? result.measurementEvents.filter(event => event && typeof event === 'object') : liveEvents;
        for (const [id, records] of [['recentEvents', events.filter(e => e.outcome !== 'running').slice(-4)], ['eventHistory', events]]) {
            const target = node(id); if (!target) continue;
            target.replaceChildren();
            if (!records.length) { target.textContent = result?.sharedResult ? 'Shared result: event history not included.' : 'No recorded observations yet.'; continue; }
            if (id === 'eventHistory' && !target.closest('details')?.open) { target.textContent = `${records.length} recorded events. Expand to inspect the full history.`; continue; }
            for (const event of records) {
                const line = root.document.createElement('div'); line.className = 'event-line'; line.dataset.outcome = event.outcome || 'observed';
                const time = root.document.createElement('time');
                const date = new Date(event.observedAt);
                const validTime = Number.isFinite(event.observedAt) && Number.isFinite(date.getTime());
                time.textContent = validTime ? `${[date.getHours(), date.getMinutes(), date.getSeconds()].map(value => String(value).padStart(2, '0')).join(':')}.${String(date.getMilliseconds()).padStart(3, '0')}` : 'Time unknown';
                if (validTime) time.dateTime = date.toISOString();
                const text = root.document.createElement('p'); text.textContent = eventDescription(event);
                line.append(time, text); target.append(line);
            }
        }
    }

    function appendEvent(event) {
        const value = snapshot(event); liveEvents.push(value);
        if (result && !result.sharedResult) {
            result.measurementEvents = snapshot(liveEvents);
            if (event.type === 'stage' && result.stageOutcomes) result.stageOutcomes[event.stage] = { ...result.stageOutcomes[event.stage], stage: event.stage, outcome: event.outcome, observedAt: event.observedAt };
        }
        renderEvents();
        if (result?.endTime != null) node('rawEvidence').textContent = JSON.stringify(result, null, 2);
    }

    function updateLive(value) {
        result = { ...result, ...snapshot(value), measurementEvents: snapshot(liveEvents) };
        inspect(inspection.section, inspection);
    }

    function open(section, options = {}) {
        node('detailsWorkspace').open = true;
        select(section, true); inspect(section, options);
        node('detailsWorkspace').scrollIntoView({ behavior: 'auto', block: 'start' });
    }

    function render() {
        const data = result;
        renderConnection(); renderEvents();
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
        if (node('acquisitionLedger')) table(node('acquisitionLedger'), windowSamples(data), [
            ['Direction', sample => sample.direction], ['Window ms', sample => number(sample.durationMs)],
            ['Payload MB', sample => Number.isFinite(sample.sizeBytes ?? sample.bytes) ? ((sample.sizeBytes ?? sample.bytes) / 1e6).toFixed(2) : null],
            ['Flows', sample => sample.concurrency], ['Mbps', sample => number(sample.mbps)]
        ]);
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

    function setResults(value) { result = snapshot(value); liveEvents = snapshot(value?.measurementEvents) || []; render(); }
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
        root.document.querySelector('.event-history')?.addEventListener('toggle', renderEvents);
        root.document.addEventListener('netspeed:chart-sample', event => {
            const { direction, index, total, sample, active } = event.detail;
            const strip = node(`${direction}MeasurementStrip`);
            if (strip) { strip.hidden = false; strip.textContent = sampleStrip(sample, index, total); }
            if (active) {
                inspect('throughput', { direction });
                fillFields(node('inspectorContent'), [['Selected window', `${index + 1}/${total}`]].concat(fields(sample).filter(([key]) => !['transfers', 'rejected transfers'].includes(key)), throughputEvidence(result, direction)));
            }
        });
        render();
    }
    const api = { snapshot, latencyEvidence, throughputEvidence, windowSamples, measurementSignature, connectionBranches, sampleStrip, eventDescription, measurementNotes, fields, setResults, updateLive, appendEvent, open, select, inspect, getResults: () => snapshot(result) };
    if (typeof module === 'object' && module.exports) module.exports = api;
    if (root.document) {
        root.NetspeedEvidence = api;
        if (root.document.readyState === 'loading') root.document.addEventListener('DOMContentLoaded', boot, { once: true });
        else boot();
    }
})(typeof window === 'object' ? window : globalThis);
