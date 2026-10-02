/* Shared measurement components, composed for each presentation's purpose. */
(function (root) {
    'use strict';
    const placeholder = '<span class="placeholder" aria-label="Not measured">—</span>';
    const value = id => `<span id="${id}">${placeholder}</span>`;
    const row = (label, id) => `<div><dt>${label}</dt><dd>${value(id)}</dd></div>`;
    const stages = [
        ['meta', 'Handshake', 'overview'], ['latency', 'Idle latency', 'latency'],
        ['download', 'Download', 'throughput'], ['upload', 'Upload', 'throughput'],
        ['loaded-latency', 'Load response', 'latency'], ['packet-loss', 'Packet path', 'packet'],
        ['complete', 'Analysis', 'overview']
    ];
    const sections = [
        ['overview', 'Overview'], ['throughput', 'Throughput'], ['latency', 'Latency'],
        ['packet', 'Packet delivery'], ['transport', 'Transport & verification'], ['raw', 'Raw evidence']
    ];

    function primaryMetrics(terminal) {
        if (terminal) return `<table class="terminal-metrics"><thead><tr><th scope="col">RESULT</th><th scope="col">DOWN</th><th scope="col">UP</th><th scope="col">RTT</th><th scope="col">JITTER</th><th scope="col">LOSS</th></tr></thead>
            <tbody><tr><th scope="row">VALUE</th><td>${value('downloadSpeed')}</td><td>${value('uploadSpeed')}</td><td>${value('latencyValue')}</td><td>${value('jitterValue')}</td><td>${value('packetLossValue')}</td></tr><tr><th scope="row">UNIT</th><td id="downloadUnit">Mbps</td><td id="uploadUnit">Mbps</td><td>ms</td><td>ms</td><td id="packetLossUnit">%</td></tr></tbody></table>`;
        return `<div class="primary-metrics">
            <div class="metric"><div class="metric-reading">${value('downloadSpeed')}<span class="metric-unit" id="downloadUnit">Mbps</span></div><p>Download <span aria-hidden="true">↓</span></p></div>
            <div class="metric"><div class="metric-reading">${value('uploadSpeed')}<span class="metric-unit" id="uploadUnit">Mbps</span></div><p>Upload <span aria-hidden="true">↑</span></p></div>
            <div class="metric metric-latency"><div class="metric-reading">${value('latencyValue')}<span class="metric-unit">ms</span></div><p>Unloaded latency</p><div class="secondary-metrics"><span>${value('jitterValue')} ms jitter</span><span>Loss ${value('packetLossValue')}<span id="packetLossUnit">%</span></span></div></div>
        </div>`;
    }

    function instrumentReadout() {
        const readings = [
            ['Download ↓', 'downloadSpeed', 'Mbps', 'downloadUnit', 'throughput', 'data-direction="download"'],
            ['Upload ↑', 'uploadSpeed', 'Mbps', 'uploadUnit', 'throughput', 'data-direction="upload"'],
            ['Unloaded latency', 'latencyValue', 'ms', '', 'latency', 'data-condition="unloaded"'],
            ['Jitter', 'jitterValue', 'ms', '', 'latency', 'data-condition="unloaded"'],
            ['Packet loss', 'packetLossValue', '%', 'packetLossUnit', 'packet', '']
        ];
        return `<dl class="instrument-readout" aria-label="Measurement readout">${readings.map(([label, id, unit, unitId, section, attributes]) => `<div class="metric"><dt><button class="figure-selection" data-inspect="${section}" ${attributes} aria-pressed="false">${label}</button></dt><dd class="metric-reading">${value(id)}<span class="metric-unit" ${unitId ? `id="${unitId}"` : ''}>${unit}</span></dd></div>`).join('')}</dl>`;
    }

    function throughput(direction, title) {
        return `<figure class="measurement-figure">
            <figcaption><h3><button class="figure-selection" data-inspect="throughput" data-direction="${direction}">${title}</button></h3><span>Transfer samples · Mbps</span></figcaption>
            <div class="throughput-plot" id="${direction}Sparkline" role="img" aria-label="${title} transfer rates"><span class="empty-figure">Awaiting measurement</span></div>
            <div class="figure-summary"><span>${value(`${direction}Sustained`)} sustained</span><span id="${direction}Range">—</span></div>
            <button class="evidence-link text-button" data-open-evidence="throughput" data-direction="${direction}" id="${direction}EvidenceLine">View transfer evidence</button>
        </figure>`;
    }

    function latency(condition, title) {
        return `<figure class="measurement-figure latency-figure" id="${condition}LatencyAccordion">
            <figcaption><h3><button class="figure-selection" data-inspect="latency" data-condition="${condition}">${title}</button></h3><span id="${condition}LatencyCount">0 samples</span></figcaption>
            <div class="latency-summary" id="${condition}LatencySummary">${placeholder}</div>
            <p class="latency-delta" id="${condition}LatencyDelta"></p>
            <div class="latency-box-plot" id="${condition}LatencyBoxPlot" data-tooltip-target="boxplot"></div>
            <button class="evidence-link text-button" data-open-evidence="latency" data-condition="${condition}" id="${condition}LatencyEvidenceLine">View sample evidence</button>
        </figure>`;
    }

    function overview() {
        return `<div id="overviewEvidence"></div><div class="extended-grid">
            <section><h3>Connection</h3><dl class="evidence-table">${row('Client IP', 'ipAddress')}${row('Network type', 'connectionType')}${row('Client location', 'clientLocation')}${row('Timezone', 'clientTimezone')}${row('Server distance', 'serverDistance')}</dl><div id="mapContainer" class="connection-map"></div></section>
            <section><h3>Quality assessment</h3><div class="quality-heading">${value('overallScore')}<span id="scoreGrade">—</span></div><p id="scoreDescription">Awaiting measurement</p>
                <dl class="evidence-table">${['bandwidth', 'latency', 'stability', 'reliability'].map(name => `<div><dt>${name[0].toUpperCase() + name.slice(1)}</dt><dd>${value(`${name}Score`)}<span class="quiet-meter"><span id="${name}Bar"></span></span></dd></div>`).join('')}</dl>
                <dl class="evidence-table">${[['Streaming', 'streamingGrade'], ['Gaming', 'gamingGrade'], ['Video calls', 'videoChatGrade']].map(([label, id]) => `<div><dt>${label}</dt><dd id="${id}" class="grade"><span class="grade-text">—</span></dd></div>`).join('')}</dl>
            </section></div>`;
    }

    function throughputDetails() {
        return `<div class="extended-grid">${['download', 'upload'].map(direction => `<section><h3>${direction === 'download' ? 'Download' : 'Upload'}</h3>
            <dl class="evidence-table">${row('Peak', `${direction}Peak`)}${row('Variability', `${direction}Variability`)}${row('Trend', `${direction}Trend`)}</dl>
            <div id="${direction}WindowEvidence"></div><div class="tests-list" id="${direction}TestsGrid"></div></section>`).join('')}</div>`;
    }

    function latencyDetails() {
        return `<div class="latency-detail-grid">${[['unloaded', 'Unloaded'], ['download', 'During download'], ['upload', 'During upload']].map(([condition, title]) => `<section><h3>${title}</h3><div id="${condition}LatencyEvidence"></div>
            <div class="latency-chart-container" id="${condition}LatencyChart"></div>
            ${condition === 'unloaded' ? `<dl class="evidence-table">${row('Minimum', 'unloadedMin')}${row('Median', 'unloadedMedian')}${row('Maximum', 'unloadedMax')}</dl>` : `<table><thead><tr><th>Sample</th><th>RTT</th></tr></thead><tbody id="${condition}LatencyTable"></tbody></table>`}
            <div id="${condition}SampleEvidence"></div></section>`).join('')}</div>`;
    }

    function packetDetails() {
        return `<div class="extended-grid"><section><h3>Directional delivery</h3><div id="packetEvidence"></div><dl class="evidence-table">${row('Minimum RTT', 'rttMin')}${row('RTT jitter', 'rttJitter')}${row('Loss pattern', 'lossTypeBadge')}${row('Loss bursts', 'burstCount')}${row('Longest burst', 'maxBurst')}${row('Average burst', 'avgBurst')}</dl><div class="loss-timeline" id="lossTimeline"></div><div class="quiet-meter"><div id="packetLossFill"></div></div></section>
            <section><h3>Selected path</h3><dl class="evidence-table">${row('Candidate', 'webrtcConnectionBadge')}${row('Protocol', 'webrtcProtocol')}${row('ICE RTT', 'iceRtt')}</dl><div id="dataChannelEvidence"></div></section></div>`;
    }

    function transportDetails() {
        return `<div class="extended-grid"><section><h3>Transport selection</h3><dl class="evidence-table transport-evidence">${row('Latency transport', 'evidenceTransport')}${row('Connection reuse', 'evidenceReuse')}${row('Discarded probes', 'evidenceDiscarded')}${row('Fallback', 'evidenceFallback')}${row('Payload / framing', 'evidencePayload')}${row('Response checks', 'evidenceVerification')}</dl><div id="transportEvidence"></div></section>
            <section><h3>Confidence</h3><span id="confidenceBadge">Not measured</span><dl class="evidence-table confidence-evidence">${[['Samples', 'sampleCount'], ['Variability', 'variability'], ['Timing', 'timing'], ['Loaded overlap', 'connection']].map(([label, id]) => `<div><dt><span id="${id}Icon" class="confidence-icon" aria-hidden="true"></span>${label}</dt><dd id="${id}Detail">Not measured</dd></div>`).join('')}</dl><div class="confidence-warnings" id="confidenceWarnings"></div><button class="text-button" id="learnMoreBtn">Measurement methodology</button></section></div>`;
    }

    function render(variant = 'standard') {
        if (!['standard', 'alternate', 'phosphor'].includes(variant)) throw new Error('Unknown presentation');
        const expert = variant === 'alternate';
        const terminal = variant === 'phosphor';
        const panels = { overview: overview(), throughput: throughputDetails(), latency: latencyDetails(), packet: packetDetails(), transport: transportDetails(), raw: '<div class="raw-actions"><button class="btn" id="copyEvidenceBtn" disabled>Copy JSON</button><button class="btn" id="downloadEvidenceBtn" disabled>Download JSON</button></div><p class="evidence-note">Full result, including capabilities, samples, rejected probes, and verification receipts. Shared links contain a compact subset.</p><pre id="rawEvidence" tabindex="0">No measurement yet.</pre>' };
        return `<div class="app-shell">
            <header class="app-nav"><a class="wordmark" href="index.html" data-interface-link="standard">${terminal ? 'NETSPEED / LINK ANALYZER' : 'NetSpeed'}</a><div class="nav-tools">${terminal ? '<time data-live-clock aria-label="Local time"></time><label class="terminal-width">Columns <select id="terminalColumns" aria-label="Terminal columns"><option value="80">80</option><option value="132">132</option></select></label>' : ''}<button class="text-button" id="themeToggle" aria-label="Toggle dark/light mode"><span class="theme-icon">Light</span> mode</button></div></header>
            <main>
                <section class="result-header" aria-labelledby="resultTitle">
                    <div class="result-heading"><div><p class="presentation-label">${expert ? 'Observatory · Evidence mode' : terminal ? 'Phosphor · Instrument mode' : 'Link measurement'}</p><h1 id="resultTitle">${expert ? 'Measurement console' : 'Network performance'}</h1></div><div class="result-state"><span data-stage-label>Ready</span><span data-progress-percent>0%</span></div></div>
                    <div class="result-context"><span id="serverLocation">Finding server…</span><span id="networkInfo">Finding network…</span><span id="testTimestamp">Not measured yet</span></div>
                    ${expert ? instrumentReadout() : primaryMetrics(terminal)}
                    <div class="measurement-notes" id="measurementNotes" role="status" hidden></div>
                    <div class="action-bar"><div class="action-buttons"><button class="btn btn-primary" id="startTestBtn"><span>Run test</span></button><button class="btn" id="pauseTestBtn" disabled><span>Pause</span></button><button class="text-button" id="shareBtn" disabled>Share result</button><button class="text-button" id="downloadResultsBtn" disabled>Export JSON</button><button class="text-button" data-open-evidence="overview">View details ↓</button></div><div class="progress-inline" id="progressContainer"><span id="progressStatus" role="status">Ready to test</span><div class="progress-track"><div id="progressFill"></div></div></div>
                    ${terminal ? '<nav class="function-keys" aria-label="Terminal shortcuts"><button data-open-evidence="overview">F1 Details</button><button data-open-evidence="throughput">F2 Throughput</button><button data-open-evidence="latency">F3 Latency</button><button data-open-evidence="packet">F4 Packet</button><button id="terminalBack">Esc Back</button></nav>' : ''}
                </section>
                <ol class="stage-rail progress-rail" aria-label="Measurement sequence">${stages.map(([stage, label, section], index) => `<li data-progress-stage="${stage}" data-label="${label}" data-outcome="pending"><button class="stage-select" data-inspect="${section}" ${stage === 'download' || stage === 'upload' ? `data-direction="${stage}"` : stage === 'latency' ? 'data-condition="unloaded"' : stage === 'loaded-latency' ? 'data-condition="download"' : ''} aria-pressed="false"><span class="stage-marker" aria-hidden="true">${index + 1}</span><b>${label}</b><span data-stage-state>Pending</span><time data-stage-time></time></button></li>`).join('')}</ol>
                <div class="measurement-layout"><div class="measurement-content">
                    <section class="measurement-section" aria-labelledby="throughputTitle"><div class="section-heading"><h2 id="throughputTitle">Throughput</h2><span>Payload rates across measured transfers</span></div><div class="throughput-grid">${throughput('download', 'Download')}${throughput('upload', 'Upload')}</div>
                        ${expert ? '<section class="acquisition-ledger" aria-labelledby="acquisitionTitle"><div class="section-heading"><h3 id="acquisitionTitle">Load-window ledger</h3><span>Aggregate payload / wall time</span></div><div id="acquisitionLedger">No load windows recorded.</div></section>' : ''}
                    </section>
                    <section class="measurement-section" aria-labelledby="latencyTitle"><div class="section-heading"><h2 id="latencyTitle">Latency under load</h2><span>Median and distribution · ms</span></div><div class="latency-grid">${latency('unloaded', 'Unloaded')}${latency('download', 'During download')}${latency('upload', 'During upload')}</div></section>
                    <section class="measurement-section measurement-ledger" aria-labelledby="deliveryTitle"><div class="section-heading"><h2 id="deliveryTitle"><button class="figure-selection" data-inspect="packet">Packet delivery</button></h2><span id="packetLossBadge">Not measured</span></div><p id="packetLossDetail">Directional delivery is measured separately from HTTP throughput.</p><dl class="delivery-summary">${row('Packets received', 'packetsReceived')}${row('Median RTT', 'rttMedian')}${row('p90 RTT', 'rttP90')}${row('Path', 'connectionPath')}</dl><button class="evidence-link text-button" data-open-evidence="packet">View packet and connection evidence</button></section>
                </div><aside class="measurement-evidence evidence-inspector" aria-labelledby="inspectorTitle"><div class="section-heading">${expert ? '<p class="inspector-kicker">Measurement inspector</p>' : ''}<h2 id="inspectorTitle">${expert ? 'Download' : 'Overview'} / evidence</h2></div><div id="inspectorContent">Select a measurement or stage to inspect its evidence.</div><button class="text-button" id="inspectorDetailsBtn" data-open-evidence="${expert ? 'throughput' : 'overview'}">Open full details ↓</button></aside></div>
                <details class="details-workspace details-section" id="detailsWorkspace" ${expert ? 'open' : ''}><summary>Details &amp; evidence</summary>
                    ${terminal ? `<label class="inspection-command">SHOW <select id="consoleSection" aria-label="Inspection section">${sections.map(([id, label]) => `<option value="${id}">${label.toUpperCase()}</option>`).join('')}</select></label>` : ''}
                    <div class="evidence-tabs" role="tablist" aria-label="Measurement details">${sections.map(([id, label], index) => `<button id="tab-${id}" role="tab" aria-controls="panel-${id}" aria-selected="${index === 0}" tabindex="${index === 0 ? 0 : -1}" data-evidence-tab="${id}">${label}</button>`).join('')}</div>
                    ${sections.map(([id]) => `<section class="evidence-panel" id="panel-${id}" role="tabpanel" aria-labelledby="tab-${id}" tabindex="0" ${id === 'overview' ? '' : 'hidden'}>${panels[id]}</section>`).join('')}
                </details>
            </main>
            <footer class="app-footer"><nav aria-label="Presentation"><a href="index.html" data-interface-link="standard" ${variant === 'standard' ? 'aria-current="page"' : ''}>Standard</a><a href="alternate.html" data-interface-link="alternate" ${expert ? 'aria-current="page"' : ''}>Observatory</a><a href="phosphor.html" data-interface-link="phosphor" ${terminal ? 'aria-current="page"' : ''}>Phosphor</a></nav><a href="https://github.com/yellowman/netspeed" target="_blank" rel="noopener">GitHub ↗</a></footer>
        </div>
        <div class="modal" id="learnMoreModal" role="dialog" aria-modal="true" aria-labelledby="methodologyTitle" hidden><div class="modal-backdrop"></div><div class="modal-content"><button class="text-button" id="modalClose" aria-label="Close methodology">Close</button><h2 id="methodologyTitle">What this test measures</h2><p>Throughput is payload delivered over measured transfers. Latency is measured on a warmed connection; loaded latency accepts probes that overlap a transfer. Packet delivery uses a separate WebRTC path.</p><p>Transport, timing, sample counts, and verification evidence help explain each result. Missing or below-resolution samples are not invented measurements.</p><p>Results describe this path and this moment, not a guarantee of future performance.</p></div></div>
        <div class="toast" id="toast" role="status"><span class="toast-message"></span></div>`;
    }

    if (typeof module === 'object' && module.exports) module.exports = { render };
    if (root.document) root.document.getElementById('app').innerHTML = render(root.document.body.dataset.interface);
})(typeof window === 'object' ? window : globalThis);
