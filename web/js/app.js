/**
 * Main application logic for netspeed UI
 */

(function() {
    'use strict';

    // Application state
    const state = {
        meta: null,
        locations: [],
        summary: null,
        quality: null,
        isRunning: false,
        isPaused: false,
        currentPhase: 'idle',
        downloadSamples: [],
        uploadSamples: [],
        latencySamples: [],
        testStartTime: null
    };

    // DOM element cache
    const elements = {};

    /**
     * Initialize the application
     */
    function init() {
        cacheElements();
        setupEventListeners();
        setupTheme();
        loadInitialData();
    }

    /**
     * Cache frequently accessed DOM elements
     */
    function cacheElements() {
        elements.startButton = document.getElementById('startTestBtn');
        elements.pauseButton = document.getElementById('pauseTestBtn');
        elements.shareButton = document.getElementById('shareBtn');
        elements.downloadButton = document.getElementById('downloadResultsBtn');
        elements.themeToggle = document.getElementById('themeToggle');

        // Hero metrics
        elements.downloadValue = document.getElementById('downloadSpeed');
        elements.downloadChart = document.getElementById('downloadChart');
        elements.uploadValue = document.getElementById('uploadSpeed');
        elements.uploadChart = document.getElementById('uploadChart');
        elements.latencyValue = document.getElementById('latencyValue');
        elements.jitterValue = document.getElementById('jitterValue');
        elements.packetLossValue = document.getElementById('packetLossValue');
        elements.measureTime = document.getElementById('testTimestamp');

        // Quality scores
        elements.streamingScore = document.getElementById('streamingGrade');
        elements.gamingScore = document.getElementById('gamingGrade');
        elements.videoChatScore = document.getElementById('videoChatGrade');

        // Server info
        elements.serverLocation = document.getElementById('serverLocation');
        elements.clientNetwork = document.getElementById('networkInfo');
        elements.clientIp = document.getElementById('ipAddress');
        elements.connectionType = document.getElementById('connectionType');

        // Latency sections
        elements.unloadedLatencyChart = document.getElementById('unloadedLatencyChart');
        elements.unloadedLatencyCount = document.getElementById('unloadedLatencyCount');
        elements.unloadedLatencySummary = document.getElementById('unloadedLatencySummary');
        elements.unloadedMin = document.getElementById('unloadedMin');
        elements.unloadedMedian = document.getElementById('unloadedMedian');
        elements.unloadedMax = document.getElementById('unloadedMax');
        elements.downloadLatencyChart = document.getElementById('downloadLatencyChart');
        elements.downloadLatencyCount = document.getElementById('downloadLatencyCount');
        elements.downloadLatencyTable = document.getElementById('downloadLatencyTable');
        elements.uploadLatencyChart = document.getElementById('uploadLatencyChart');
        elements.uploadLatencyCount = document.getElementById('uploadLatencyCount');
        elements.uploadLatencyTable = document.getElementById('uploadLatencyTable');

        // Packet loss
        elements.packetLossFill = document.getElementById('packetLossFill');
        elements.packetLossBadge = document.getElementById('packetLossBadge');
        elements.packetLossDetail = document.getElementById('packetLossDetail');
        elements.packetsReceived = document.getElementById('packetsReceived');
        elements.rttMin = document.getElementById('rttMin');
        elements.rttMedian = document.getElementById('rttMedian');
        elements.rttP90 = document.getElementById('rttP90');
        elements.rttJitter = document.getElementById('rttJitter');

        // Test grids
        elements.downloadGrid = document.getElementById('downloadTestsGrid');
        elements.uploadGrid = document.getElementById('uploadTestsGrid');

        // Progress
        elements.progressContainer = document.getElementById('progressContainer');
        elements.progressFill = document.getElementById('progressFill');
        elements.progressStatus = document.getElementById('progressStatus');

        // Modal
        elements.learnMoreBtn = document.getElementById('learnMoreBtn');
        elements.learnMoreModal = document.getElementById('learnMoreModal');
        elements.modalClose = document.getElementById('modalClose');
        elements.toast = document.getElementById('toast');
    }

    /**
     * Setup event listeners
     */
    function setupEventListeners() {
        elements.startButton?.addEventListener('click', startTest);
        elements.pauseButton?.addEventListener('click', togglePause);
        elements.shareButton?.addEventListener('click', shareResults);
        elements.downloadButton?.addEventListener('click', downloadResults);
        elements.themeToggle?.addEventListener('click', toggleTheme);

        // Accordion toggles
        document.querySelectorAll('.accordion-header').forEach(header => {
            header.addEventListener('click', () => {
                const accordion = header.closest('.accordion');
                const isExpanded = header.getAttribute('aria-expanded') === 'true';
                header.setAttribute('aria-expanded', !isExpanded);
                accordion.classList.toggle('expanded');
            });
        });

        // Learn more modal
        elements.learnMoreBtn?.addEventListener('click', () => openModal('learnMoreModal'));
        elements.modalClose?.addEventListener('click', closeModals);
        elements.learnMoreModal?.querySelector('.modal-backdrop')?.addEventListener('click', closeModals);

        // Escape key to close modal
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') closeModals();
        });
    }

    /**
     * Setup theme based on user preference
     */
    function setupTheme() {
        const savedTheme = localStorage.getItem('theme');
        const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        const theme = savedTheme || (prefersDark ? 'dark' : 'light');

        document.documentElement.setAttribute('data-theme', theme);
        updateThemeIcon(theme);
    }

    /**
     * Toggle between light and dark themes
     */
    function toggleTheme() {
        const currentTheme = document.documentElement.getAttribute('data-theme');
        const newTheme = currentTheme === 'dark' ? 'light' : 'dark';

        document.documentElement.setAttribute('data-theme', newTheme);
        localStorage.setItem('theme', newTheme);
        updateThemeIcon(newTheme);
    }

    /**
     * Update theme toggle icon
     */
    function updateThemeIcon(theme) {
        const icon = elements.themeToggle?.querySelector('.theme-icon');
        if (icon) {
            icon.textContent = theme === 'dark' ? '\u263E' : '\u2600';
        }
    }

    /**
     * Load initial metadata and locations
     */
    async function loadInitialData() {
        try {
            const [meta, locations] = await Promise.all([
                SpeedTest.fetchMeta(),
                SpeedTest.fetchLocations()
            ]);

            state.meta = meta;
            state.locations = locations;

            updateServerInfo();
        } catch (err) {
            console.error('Failed to load initial data:', err);
        }
    }

    /**
     * Start the speed test
     */
    async function startTest() {
        if (state.isRunning) return;

        state.isRunning = true;
        state.testStartTime = Date.now();
        resetResults();
        updateUIState('running');

        SpeedTest.setCallbacks({
            onProgress: handleProgress,
            onMetaReceived: handleMetaReceived,
            onDownloadProgress: handleDownloadProgress,
            onUploadProgress: handleUploadProgress,
            onLatencyProgress: handleLatencyProgress,
            onPacketLossProgress: handlePacketLossProgress,
            onComplete: handleComplete,
            onError: handleError
        });

        try {
            await SpeedTest.start();
        } catch (err) {
            if (err.name !== 'AbortError') {
                console.error('Speed test failed:', err);
                showError('Speed test failed. Please try again.');
            }
        }

        state.isRunning = false;
        updateUIState('complete');
    }

    /**
     * Toggle pause state
     */
    function togglePause() {
        if (!state.isRunning) return;

        state.isPaused = !state.isPaused;

        if (state.isPaused) {
            SpeedTest.pause();
            elements.pauseButton.textContent = 'Resume';
        } else {
            SpeedTest.resume();
            elements.pauseButton.textContent = 'Pause';
        }
    }

    /**
     * Retest
     */
    function retest() {
        if (state.isRunning) {
            SpeedTest.stop();
        }
        setTimeout(startTest, 100);
    }

    /**
     * Reset results display
     */
    function resetResults() {
        state.downloadSamples = [];
        state.uploadSamples = [];
        state.latencySamples = [];
        state.summary = null;
        state.quality = null;

        // Reset hero values
        if (elements.downloadValue) elements.downloadValue.textContent = '--';
        if (elements.uploadValue) elements.uploadValue.textContent = '--';
        if (elements.latencyValue) elements.latencyValue.textContent = '--';
        if (elements.jitterValue) elements.jitterValue.textContent = '-- ms';
        if (elements.packetLossValue) elements.packetLossValue.textContent = '--%';

        // Clear sparklines
        if (elements.downloadSparkline) elements.downloadSparkline.innerHTML = '';
        if (elements.uploadSparkline) elements.uploadSparkline.innerHTML = '';

        // Reset quality scores
        ['streaming', 'gaming', 'videoChat'].forEach(type => {
            const el = elements[`${type}Score`];
            if (el) {
                el.className = 'quality-grade';
                el.textContent = '--';
            }
        });

        // Clear test grids
        if (elements.downloadGrid) elements.downloadGrid.innerHTML = '';
        if (elements.uploadGrid) elements.uploadGrid.innerHTML = '';

        // Clear latency charts
        ['unloaded', 'download', 'upload'].forEach(phase => {
            const chartEl = elements[`${phase}LatencyChart`];
            if (chartEl) chartEl.innerHTML = '';
        });
    }

    /**
     * Update UI state
     */
    function updateUIState(uiState) {
        const isRunning = uiState === 'running';
        const isComplete = uiState === 'complete';

        if (elements.startButton) {
            elements.startButton.disabled = isRunning;
            if (isRunning) {
                elements.startButton.querySelector('span').textContent = 'Running...';
            } else {
                elements.startButton.querySelector('span').textContent = isComplete ? 'Retest' : 'Start Test';
            }
        }

        if (elements.pauseButton) {
            elements.pauseButton.disabled = !isRunning;
        }

        if (elements.shareButton) {
            elements.shareButton.disabled = !isComplete;
        }

        if (elements.downloadButton) {
            elements.downloadButton.disabled = !isComplete;
        }

        if (elements.progressContainer) {
            elements.progressContainer.classList.toggle('active', isRunning);
        }

        if (elements.progressStatus) {
            if (!isRunning && !isComplete) {
                elements.progressStatus.textContent = 'Ready to test';
            } else if (isComplete) {
                elements.progressStatus.textContent = 'Test complete';
            }
        }

        if (elements.progressFill && !isRunning && !isComplete) {
            elements.progressFill.style.width = '0%';
        }
    }

    /**
     * Handle progress updates
     */
    function handleProgress(phase, progress) {
        state.currentPhase = phase;

        const phaseLabels = {
            'meta': 'Loading metadata...',
            'latency': 'Measuring latency...',
            'download': 'Testing download speed...',
            'upload': 'Testing upload speed...',
            'loaded-latency': 'Measuring loaded latency...',
            'packet-loss': 'Testing packet loss...'
        };

        if (elements.progressText) {
            elements.progressText.textContent = phaseLabels[phase] || 'Running tests...';
        }
    }

    /**
     * Handle metadata received
     */
    function handleMetaReceived(meta, locations) {
        state.meta = meta;
        state.locations = locations;
        updateServerInfo();
    }

    /**
     * Handle download progress
     */
    function handleDownloadProgress(profile, run, totalRuns, sample, totalComplete, totalExpected) {
        state.downloadSamples.push(sample);

        // Update hero value with latest reading
        const avgSpeed = calculateAverageSpeed(state.downloadSamples);
        updateHeroValue('download', avgSpeed);

        // Update sparkline
        updateDownloadSparkline();

        // Update test grid
        updateTestGrid('download', profile, run, totalRuns, sample);

        // Update progress
        updateProgress(totalComplete, totalExpected, 'download');
    }

    /**
     * Handle upload progress
     */
    function handleUploadProgress(profile, run, totalRuns, sample, totalComplete, totalExpected) {
        state.uploadSamples.push(sample);

        // Update hero value with latest reading
        const avgSpeed = calculateAverageSpeed(state.uploadSamples);
        updateHeroValue('upload', avgSpeed);

        // Update sparkline
        updateUploadSparkline();

        // Update test grid
        updateTestGrid('upload', profile, run, totalRuns, sample);

        // Update progress
        updateProgress(totalComplete, totalExpected, 'upload');
    }

    /**
     * Handle latency progress
     */
    function handleLatencyProgress(phase, current, total, sample) {
        state.latencySamples.push(sample);

        const phaseSamples = state.latencySamples.filter(s => s.phase === phase);
        const values = phaseSamples.map(s => s.rttMs);

        if (phase === 'unloaded') {
            // Update hero latency value
            const medianLatency = Charts.median(values);
            if (elements.latencyValue) {
                elements.latencyValue.textContent = medianLatency.toFixed(1);
            }

            // Update count badge
            if (elements.unloadedLatencyCount) {
                elements.unloadedLatencyCount.textContent = `${current}/${total}`;
            }

            // Update summary
            if (elements.unloadedLatencySummary && values.length > 0) {
                const min = Math.min(...values);
                const max = Math.max(...values);
                elements.unloadedLatencySummary.textContent = `${min.toFixed(1)} - ${max.toFixed(1)} ms`;
            }

            // Update stats
            if (values.length > 0) {
                const min = Math.min(...values);
                const max = Math.max(...values);
                const median = Charts.median(values);
                if (elements.unloadedMin) elements.unloadedMin.textContent = `${min.toFixed(1)} ms`;
                if (elements.unloadedMedian) elements.unloadedMedian.textContent = `${median.toFixed(1)} ms`;
                if (elements.unloadedMax) elements.unloadedMax.textContent = `${max.toFixed(1)} ms`;
            }
        } else if (phase === 'download') {
            // Update count badge
            if (elements.downloadLatencyCount) {
                elements.downloadLatencyCount.textContent = `${current}/5`;
            }

            // Update table
            if (elements.downloadLatencyTable) {
                const row = document.createElement('tr');
                row.innerHTML = `<td>${current}</td><td>${sample.rttMs.toFixed(1)} ms</td>`;
                elements.downloadLatencyTable.appendChild(row);
            }
        } else if (phase === 'upload') {
            // Update count badge
            if (elements.uploadLatencyCount) {
                elements.uploadLatencyCount.textContent = `${current}/5`;
            }

            // Update table
            if (elements.uploadLatencyTable) {
                const row = document.createElement('tr');
                row.innerHTML = `<td>${current}</td><td>${sample.rttMs.toFixed(1)} ms</td>`;
                elements.uploadLatencyTable.appendChild(row);
            }
        }
    }

    /**
     * Handle packet loss progress
     */
    function handlePacketLossProgress(sent, total, received) {
        const lossPercent = ((sent - received) / sent * 100);

        if (elements.packetLossBadge) {
            elements.packetLossBadge.textContent = `${received}/${sent}`;
        }

        if (elements.packetLossFill) {
            elements.packetLossFill.style.width = `${(received / sent) * 100}%`;
        }

        if (elements.packetLossDetail) {
            elements.packetLossDetail.textContent = `${lossPercent.toFixed(2)}%`;
        }

        if (elements.packetsReceived) {
            elements.packetsReceived.textContent = `${received} / ${sent} packets`;
        }
    }

    /**
     * Handle test completion
     */
    function handleComplete(results, summary, quality) {
        state.summary = summary;
        state.quality = quality;

        // Update final hero values
        updateHeroValue('download', summary.downloadMbps);
        updateHeroValue('upload', summary.uploadMbps);

        if (elements.latencyValue) {
            elements.latencyValue.textContent = summary.latencyUnloadedMs.toFixed(1);
        }

        if (elements.jitterValue) {
            elements.jitterValue.textContent = `${summary.jitterMs.toFixed(1)} ms`;
        }

        if (elements.packetLossValue) {
            elements.packetLossValue.textContent = `${summary.packetLossPercent.toFixed(2)}%`;
        }

        // Update measure time
        if (elements.measureTime) {
            elements.measureTime.textContent = `Measured at ${formatTime(new Date())}`;
        }

        // Update quality scores
        updateQualityScores(quality);

        // Update packet loss details
        if (results.packetLoss) {
            updatePacketLossDetails(results.packetLoss);
        }

        // Hide progress indicator
        if (elements.progressIndicator) {
            elements.progressIndicator.style.display = 'none';
        }
    }

    /**
     * Handle errors
     */
    function handleError(type, profile, run, error) {
        console.error(`Error in ${type} test:`, profile, run, error);

        // Mark the relevant test as failed
        if (type === 'download' || type === 'upload') {
            const grid = elements[`${type}Grid`];
            if (grid) {
                const card = grid.querySelector(`[data-profile="${profile}"]`);
                if (card) {
                    card.classList.add('error');
                }
            }
        }
    }

    /**
     * Update server info display
     */
    function updateServerInfo() {
        if (!state.meta) return;

        // Find matching location
        const serverLocation = state.locations.find(l => l.iata === state.meta.colo);

        if (elements.serverLocation) {
            elements.serverLocation.textContent = serverLocation
                ? `${serverLocation.city}, ${serverLocation.cca2}`
                : state.meta.colo;
        }

        if (elements.clientNetwork) {
            elements.clientNetwork.textContent = `${state.meta.asOrganization} (AS${state.meta.asn})`;
        }

        if (elements.clientIp) {
            elements.clientIp.textContent = state.meta.clientIp;
        }

        if (elements.connectionType) {
            const isIPv6 = state.meta.clientIp.includes(':');
            elements.connectionType.textContent = isIPv6 ? 'IPv6' : 'IPv4';
        }
    }

    /**
     * Update hero value with proper formatting
     */
    function updateHeroValue(type, value) {
        const valueEl = elements[`${type}Value`];
        const unitEl = elements[`${type}Unit`];

        if (!valueEl) return;

        if (value >= 1000) {
            valueEl.textContent = (value / 1000).toFixed(2);
            if (unitEl) unitEl.textContent = 'Gbps';
        } else {
            valueEl.textContent = value.toFixed(1);
            if (unitEl) unitEl.textContent = 'Mbps';
        }
    }

    /**
     * Update download sparkline
     */
    function updateDownloadSparkline() {
        if (!elements.downloadSparkline || state.downloadSamples.length < 2) return;

        const speeds = state.downloadSamples.map(s => s.mbps);
        Charts.sparkline(elements.downloadSparkline, speeds, {
            width: 100,
            height: 30,
            strokeColor: 'var(--accent-primary)'
        });
    }

    /**
     * Update upload sparkline
     */
    function updateUploadSparkline() {
        if (!elements.uploadSparkline || state.uploadSamples.length < 2) return;

        const speeds = state.uploadSamples.map(s => s.mbps);
        Charts.sparkline(elements.uploadSparkline, speeds, {
            width: 100,
            height: 30,
            strokeColor: 'var(--accent-secondary)'
        });
    }

    /**
     * Update test grid cards
     */
    function updateTestGrid(type, profile, run, totalRuns, sample) {
        const grid = elements[`${type}Grid`];
        if (!grid) return;

        let card = grid.querySelector(`[data-profile="${profile}"]`);

        if (!card) {
            card = createTestCard(type, profile, totalRuns);
            grid.appendChild(card);
        }

        // Update run count
        const runCount = card.querySelector('.run-count');
        if (runCount) {
            runCount.textContent = `(${run}/${totalRuns})`;
        }

        // Update speed value
        const speedValue = card.querySelector('.test-speed');
        if (speedValue) {
            speedValue.textContent = `${sample.mbps.toFixed(1)} Mbps`;
        }

        // Add to chart data
        const chartContainer = card.querySelector('.test-chart');
        if (chartContainer) {
            const profileSamples = (type === 'download' ? state.downloadSamples : state.uploadSamples)
                .filter(s => s.profile === profile)
                .map(s => s.mbps);

            Charts.sparkline(chartContainer, profileSamples, {
                width: chartContainer.clientWidth || 150,
                height: 40,
                strokeColor: type === 'download' ? 'var(--accent-primary)' : 'var(--accent-secondary)'
            });
        }

        // Update table
        const tableBody = card.querySelector('.test-table tbody');
        if (tableBody) {
            const row = document.createElement('tr');
            row.innerHTML = `
                <td>${run}</td>
                <td>${sample.durationMs.toFixed(0)} ms</td>
                <td>${sample.mbps.toFixed(1)} Mbps</td>
            `;
            tableBody.appendChild(row);
        }
    }

    /**
     * Create a test card element
     */
    function createTestCard(type, profile, totalRuns) {
        const sizeLabel = formatProfileSize(profile);
        const card = document.createElement('div');
        card.className = 'test-card accordion-item';
        card.dataset.profile = profile;

        card.innerHTML = `
            <div class="accordion-header">
                <div class="accordion-title">
                    <span>${sizeLabel} ${type} test</span>
                    <span class="run-count">(0/${totalRuns})</span>
                </div>
                <span class="test-speed">--</span>
                <span class="accordion-icon"></span>
            </div>
            <div class="accordion-content">
                <div class="test-chart"></div>
                <table class="test-table">
                    <thead>
                        <tr>
                            <th>#</th>
                            <th>Duration</th>
                            <th>Speed</th>
                        </tr>
                    </thead>
                    <tbody></tbody>
                </table>
            </div>
        `;

        // Add click handler for accordion
        card.querySelector('.accordion-header').addEventListener('click', () => {
            card.classList.toggle('expanded');
        });

        return card;
    }

    /**
     * Update latency table
     */
    function updateLatencyTable(phase, samples) {
        const tableEl = elements[`${phase}LatencyTable`];
        if (!tableEl) return;

        tableEl.innerHTML = `
            <table class="latency-table">
                <thead>
                    <tr>
                        <th>#</th>
                        <th>Ping</th>
                    </tr>
                </thead>
                <tbody>
                    ${samples.map((s, i) => `
                        <tr>
                            <td>${i + 1}</td>
                            <td>${s.rttMs.toFixed(1)} ms</td>
                        </tr>
                    `).join('')}
                </tbody>
            </table>
        `;
    }

    /**
     * Update accordion header text
     */
    function updateAccordionHeader(id, text) {
        const header = document.querySelector(`#${id} .accordion-header .run-count,
                                              [data-section="${id}"] .run-count`);
        if (header) {
            header.textContent = text;
        }
    }

    /**
     * Update quality scores display
     */
    function updateQualityScores(quality) {
        const gradeClass = {
            'Great': 'great',
            'Good': 'good',
            'Okay': 'okay',
            'Poor': 'poor'
        };

        // Video Streaming
        if (elements.streamingScore) {
            const grade = quality.videoStreaming;
            elements.streamingScore.className = `quality-grade ${gradeClass[grade] || ''}`;
            const dot = elements.streamingScore.querySelector('.grade-dot');
            const text = elements.streamingScore.querySelector('.grade-text');
            if (text) text.textContent = grade || '--';
        }

        // Gaming
        if (elements.gamingScore) {
            const grade = quality.gaming;
            elements.gamingScore.className = `quality-grade ${gradeClass[grade] || ''}`;
            const text = elements.gamingScore.querySelector('.grade-text');
            if (text) text.textContent = grade || '--';
        }

        // Video Chatting
        if (elements.videoChatScore) {
            const grade = quality.videoChatting;
            elements.videoChatScore.className = `quality-grade ${gradeClass[grade] || ''}`;
            const text = elements.videoChatScore.querySelector('.grade-text');
            if (text) text.textContent = grade || '--';
        }
    }

    /**
     * Update packet loss details
     */
    function updatePacketLossDetails(packetLoss) {
        // Update badge
        if (elements.packetLossBadge) {
            elements.packetLossBadge.textContent = `${packetLoss.received}/${packetLoss.sent}`;
        }

        // Update fill bar
        if (elements.packetLossFill) {
            const successPercent = (packetLoss.received / packetLoss.sent) * 100;
            elements.packetLossFill.style.width = `${successPercent}%`;
        }

        // Update detail text
        if (elements.packetLossDetail) {
            elements.packetLossDetail.textContent = `${packetLoss.lossPercent.toFixed(2)}%`;
        }

        if (elements.packetsReceived) {
            elements.packetsReceived.textContent = `${packetLoss.received} / ${packetLoss.sent} packets`;
        }

        // Update RTT stats
        if (elements.rttMin) {
            elements.rttMin.textContent = `${packetLoss.rttStatsMs.min.toFixed(1)} ms`;
        }
        if (elements.rttMedian) {
            elements.rttMedian.textContent = `${packetLoss.rttStatsMs.median.toFixed(1)} ms`;
        }
        if (elements.rttP90) {
            elements.rttP90.textContent = `${packetLoss.rttStatsMs.p90.toFixed(1)} ms`;
        }
        if (elements.rttJitter) {
            elements.rttJitter.textContent = `${packetLoss.jitterMs.toFixed(1)} ms`;
        }
    }

    /**
     * Update progress indicator
     */
    function updateProgress(current, total, phase) {
        if (elements.progressFill) {
            const percent = (current / total) * 100;
            elements.progressFill.style.width = `${percent}%`;
        }

        if (elements.progressStatus) {
            const phaseLabel = phase === 'download' ? 'download' : 'upload';
            elements.progressStatus.textContent = `Testing ${phaseLabel}... (${current}/${total})`;
        }
    }

    /**
     * Calculate average speed from samples
     */
    function calculateAverageSpeed(samples) {
        if (samples.length === 0) return 0;
        const speeds = samples.map(s => s.mbps);
        return speeds.reduce((a, b) => a + b, 0) / speeds.length;
    }

    /**
     * Format profile size for display
     */
    function formatProfileSize(profile) {
        const sizes = {
            '100k': '100 KB',
            '1M': '1 MB',
            '10M': '10 MB',
            '25M': '25 MB',
            '50M': '50 MB',
            '100M': '100 MB'
        };
        return sizes[profile] || profile;
    }

    /**
     * Format time for display
     */
    function formatTime(date) {
        return date.toLocaleTimeString('en-US', {
            hour: '2-digit',
            minute: '2-digit',
            second: '2-digit'
        });
    }

    /**
     * Share results
     */
    async function shareResults() {
        if (!state.summary) return;

        const shareData = {
            title: 'Speed Test Results',
            text: `Download: ${state.summary.downloadMbps.toFixed(1)} Mbps\n` +
                  `Upload: ${state.summary.uploadMbps.toFixed(1)} Mbps\n` +
                  `Latency: ${state.summary.latencyUnloadedMs.toFixed(1)} ms`,
            url: window.location.href
        };

        if (navigator.share) {
            try {
                await navigator.share(shareData);
            } catch (err) {
                if (err.name !== 'AbortError') {
                    copyToClipboard(shareData.text);
                }
            }
        } else {
            copyToClipboard(shareData.text);
        }
    }

    /**
     * Download results as JSON
     */
    function downloadResults() {
        const json = SpeedTest.exportResults();
        const blob = new Blob([json], { type: 'application/json' });
        const url = URL.createObjectURL(blob);

        const a = document.createElement('a');
        a.href = url;
        a.download = `speedtest-results-${Date.now()}.json`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
    }

    /**
     * Copy text to clipboard
     */
    function copyToClipboard(text) {
        navigator.clipboard.writeText(text).then(() => {
            showNotification('Results copied to clipboard');
        }).catch(() => {
            showNotification('Failed to copy results', 'error');
        });
    }

    /**
     * Show notification
     */
    function showNotification(message, type = 'success') {
        const notification = document.createElement('div');
        notification.className = `notification notification-${type}`;
        notification.textContent = message;

        document.body.appendChild(notification);

        setTimeout(() => {
            notification.classList.add('show');
        }, 10);

        setTimeout(() => {
            notification.classList.remove('show');
            setTimeout(() => notification.remove(), 300);
        }, 3000);
    }

    /**
     * Show error message
     */
    function showError(message) {
        showNotification(message, 'error');
    }

    /**
     * Open modal
     */
    function openModal(modalId) {
        const modal = document.getElementById(modalId);
        if (modal) {
            modal.classList.add('active');
        }
    }

    /**
     * Close all modals
     */
    function closeModals() {
        document.querySelectorAll('.modal.active').forEach(modal => {
            modal.classList.remove('active');
        });
    }

    // Initialize when DOM is ready
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    // Expose for debugging
    window.NetspeedApp = {
        state,
        startTest,
        retest,
        SpeedTest,
        Charts
    };
})();
