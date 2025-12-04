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
        testStartTime: null,
        timingWarningShown: false,
        timingFallbackCount: 0,
        mapRendered: false
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

        // Check if viewing shared results
        const isSharedView = checkForSharedResults();

        // Load server info (unless viewing shared results)
        if (!isSharedView) {
            loadInitialData();
        }
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
        elements.downloadUnit = document.getElementById('downloadUnit');
        elements.downloadSparkline = document.getElementById('downloadSparkline');
        elements.uploadValue = document.getElementById('uploadSpeed');
        elements.uploadUnit = document.getElementById('uploadUnit');
        elements.uploadSparkline = document.getElementById('uploadSparkline');
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
        elements.mapContainer = document.getElementById('mapContainer');

        // Latency sections
        elements.unloadedLatencyChart = document.getElementById('unloadedLatencyChart');
        elements.unloadedLatencyBoxPlot = document.getElementById('unloadedLatencyBoxPlot');
        elements.unloadedLatencyCount = document.getElementById('unloadedLatencyCount');
        elements.unloadedLatencySummary = document.getElementById('unloadedLatencySummary');
        elements.unloadedMin = document.getElementById('unloadedMin');
        elements.unloadedMedian = document.getElementById('unloadedMedian');
        elements.unloadedMax = document.getElementById('unloadedMax');
        elements.downloadLatencyChart = document.getElementById('downloadLatencyChart');
        elements.downloadLatencyBoxPlot = document.getElementById('downloadLatencyBoxPlot');
        elements.downloadLatencyCount = document.getElementById('downloadLatencyCount');
        elements.downloadLatencySummary = document.getElementById('downloadLatencySummary');
        elements.downloadLatencyTable = document.getElementById('downloadLatencyTable');
        elements.uploadLatencyChart = document.getElementById('uploadLatencyChart');
        elements.uploadLatencyBoxPlot = document.getElementById('uploadLatencyBoxPlot');
        elements.uploadLatencyCount = document.getElementById('uploadLatencyCount');
        elements.uploadLatencySummary = document.getElementById('uploadLatencySummary');
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

        // Extended location
        elements.clientLocation = document.getElementById('clientLocation');
        elements.clientTimezone = document.getElementById('clientTimezone');
        elements.serverDistance = document.getElementById('serverDistance');

        // Loss pattern analysis
        elements.lossTypeBadge = document.getElementById('lossTypeBadge');
        elements.lossTimeline = document.getElementById('lossTimeline');
        elements.burstCount = document.getElementById('burstCount');
        elements.maxBurst = document.getElementById('maxBurst');
        elements.avgBurst = document.getElementById('avgBurst');

        // Data channel stats (simplified)
        elements.webrtcConnectionBadge = document.getElementById('webrtcConnectionBadge');
        elements.connectionPath = document.getElementById('connectionPath');
        elements.webrtcProtocol = document.getElementById('webrtcProtocol');
        elements.iceRtt = document.getElementById('iceRtt');

        // Bandwidth estimation
        elements.downloadTrend = document.getElementById('downloadTrend');
        elements.downloadPeak = document.getElementById('downloadPeak');
        elements.downloadSustained = document.getElementById('downloadSustained');
        elements.downloadVariability = document.getElementById('downloadVariability');
        elements.uploadTrend = document.getElementById('uploadTrend');
        elements.uploadPeak = document.getElementById('uploadPeak');
        elements.uploadSustained = document.getElementById('uploadSustained');
        elements.uploadVariability = document.getElementById('uploadVariability');

        // Network quality score
        elements.overallScore = document.getElementById('overallScore');
        elements.scoreGrade = document.getElementById('scoreGrade');
        elements.scoreDescription = document.getElementById('scoreDescription');
        elements.bandwidthBar = document.getElementById('bandwidthBar');
        elements.bandwidthScore = document.getElementById('bandwidthScore');
        elements.latencyBar = document.getElementById('latencyBar');
        elements.latencyScore = document.getElementById('latencyScore');
        elements.stabilityBar = document.getElementById('stabilityBar');
        elements.stabilityScore = document.getElementById('stabilityScore');
        elements.reliabilityBar = document.getElementById('reliabilityBar');
        elements.reliabilityScore = document.getElementById('reliabilityScore');

        // Test confidence
        elements.confidenceBadge = document.getElementById('confidenceBadge');
        elements.sampleCountIcon = document.getElementById('sampleCountIcon');
        elements.sampleCountDetail = document.getElementById('sampleCountDetail');
        elements.variabilityIcon = document.getElementById('variabilityIcon');
        elements.variabilityDetail = document.getElementById('variabilityDetail');
        elements.timingIcon = document.getElementById('timingIcon');
        elements.timingDetail = document.getElementById('timingDetail');
        elements.connectionIcon = document.getElementById('connectionIcon');
        elements.connectionDetail = document.getElementById('connectionDetail');
        elements.confidenceWarnings = document.getElementById('confidenceWarnings');

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

        // Latency details toggles
        document.querySelectorAll('.latency-details-toggle').forEach(toggle => {
            toggle.addEventListener('click', () => {
                const isExpanded = toggle.getAttribute('aria-expanded') === 'true';
                toggle.setAttribute('aria-expanded', !isExpanded);
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

        // Setup box plot tooltips
        setupBoxPlotTooltips();
    }

    /**
     * Setup tooltips for box plot elements
     */
    function setupBoxPlotTooltips() {
        // Create tooltip element
        const tooltip = document.createElement('div');
        tooltip.className = 'boxplot-tooltip';
        tooltip.innerHTML = `
            <h4>Distribution Chart</h4>
            <ul>
                <li><strong>Thick bar:</strong> Middle 50% of values (25th-75th percentile)</li>
                <li><strong>Solid line:</strong> Median (middle value)</li>
                <li><strong>Dashed line:</strong> Average (mean)</li>
                <li><strong>Whiskers:</strong> Full range (min to max)</li>
                <li><strong>End caps:</strong> Minimum and maximum values</li>
            </ul>
        `;
        document.body.appendChild(tooltip);

        // Event delegation for box plot tooltips
        document.addEventListener('mouseenter', (e) => {
            const target = e.target.closest('[data-tooltip-target="boxplot"]');
            if (target) {
                const rect = target.getBoundingClientRect();
                tooltip.style.left = `${rect.left}px`;
                tooltip.style.top = `${rect.bottom + 8}px`;
                tooltip.classList.add('visible');
            }
        }, true);

        document.addEventListener('mouseleave', (e) => {
            const target = e.target.closest('[data-tooltip-target="boxplot"]');
            if (target) {
                tooltip.classList.remove('visible');
            }
        }, true);
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
     * Show a toast notification
     */
    function showToast(message, duration = 4000) {
        if (!elements.toast) return;

        const messageEl = elements.toast.querySelector('.toast-message');
        if (messageEl) {
            messageEl.textContent = message;
        }

        elements.toast.classList.add('show');

        setTimeout(() => {
            elements.toast.classList.remove('show');
        }, duration);
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
        state.timingWarningShown = false;
        state.timingFallbackCount = 0;
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
            onError: handleError,
            onTimingWarning: handleTimingWarning
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
        state.packetLoss = null;
        state.networkScore = null;

        // Reset hero values with shimmer placeholders
        const ph = '<span class="placeholder"></span>';
        if (elements.downloadValue) elements.downloadValue.innerHTML = ph;
        if (elements.uploadValue) elements.uploadValue.innerHTML = ph;
        if (elements.downloadUnit) elements.downloadUnit.textContent = 'Mbps';
        if (elements.uploadUnit) elements.uploadUnit.textContent = 'Mbps';
        if (elements.latencyValue) elements.latencyValue.innerHTML = ph;
        if (elements.jitterValue) elements.jitterValue.innerHTML = ph;
        if (elements.packetLossValue) elements.packetLossValue.innerHTML = ph;

        // Clear sparklines
        if (elements.downloadSparkline) elements.downloadSparkline.innerHTML = '';
        if (elements.uploadSparkline) elements.uploadSparkline.innerHTML = '';

        // Reset quality scores
        ['streaming', 'gaming', 'videoChat'].forEach(type => {
            const el = elements[`${type}Score`];
            if (el) {
                el.className = 'quality-grade';
                const text = el.querySelector('.grade-text');
                if (text) text.innerHTML = ph;
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
            'warmup': 'Warming up connection...',
            'download': 'Testing download speed...',
            'upload': 'Testing upload speed...',
            'loaded-latency': 'Measuring loaded latency...',
            'packet-loss': 'Packet Loss Test (0/1000)'
        };

        if (elements.progressText) {
            elements.progressText.textContent = phaseLabels[phase] || 'Running tests...';
        }

        // Update main status bar for phase changes
        if (elements.progressStatus) {
            elements.progressStatus.textContent = phaseLabels[phase] || 'Running tests...';
        }

        // Reset progress bar at start of new phase
        if (elements.progressFill && phase === 'packet-loss') {
            elements.progressFill.style.width = '0%';
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

                // Update box plot - use parent width for reliability
                if (elements.unloadedLatencyBoxPlot && values.length >= 2) {
                    const parentWidth = elements.unloadedLatencyBoxPlot.parentElement?.offsetWidth || 300;
                    Charts.boxPlot(elements.unloadedLatencyBoxPlot, values, {
                        width: Math.max(200, parentWidth - 16),
                        height: 60,
                        barColor: 'var(--color-latency)',
                        unit: 'ms'
                    });
                }
            }
        } else if (phase === 'download') {
            // Update count badge
            if (elements.downloadLatencyCount) {
                elements.downloadLatencyCount.textContent = `${current}/5`;
            }

            // Update summary
            if (elements.downloadLatencySummary && values.length > 0) {
                const min = Math.min(...values);
                const max = Math.max(...values);
                elements.downloadLatencySummary.textContent = `${min.toFixed(1)} - ${max.toFixed(1)} ms`;
            }

            // Update table
            if (elements.downloadLatencyTable) {
                const row = document.createElement('tr');
                row.innerHTML = `<td>${current}</td><td>${sample.rttMs.toFixed(1)} ms</td>`;
                elements.downloadLatencyTable.appendChild(row);
            }

            // Update box plot - use parent width for reliability
            if (elements.downloadLatencyBoxPlot && values.length >= 2) {
                const parentWidth = elements.downloadLatencyBoxPlot.parentElement?.offsetWidth || 300;
                Charts.boxPlot(elements.downloadLatencyBoxPlot, values, {
                    width: Math.max(200, parentWidth - 16),
                    height: 60,
                    barColor: 'var(--color-download)',
                    unit: 'ms'
                });
            }
        } else if (phase === 'upload') {
            // Update count badge
            if (elements.uploadLatencyCount) {
                elements.uploadLatencyCount.textContent = `${current}/5`;
            }

            // Update summary
            if (elements.uploadLatencySummary && values.length > 0) {
                const min = Math.min(...values);
                const max = Math.max(...values);
                elements.uploadLatencySummary.textContent = `${min.toFixed(1)} - ${max.toFixed(1)} ms`;
            }

            // Update table
            if (elements.uploadLatencyTable) {
                const row = document.createElement('tr');
                row.innerHTML = `<td>${current}</td><td>${sample.rttMs.toFixed(1)} ms</td>`;
                elements.uploadLatencyTable.appendChild(row);
            }

            // Update box plot - use parent width for reliability
            if (elements.uploadLatencyBoxPlot && values.length >= 2) {
                const parentWidth = elements.uploadLatencyBoxPlot.parentElement?.offsetWidth || 300;
                Charts.boxPlot(elements.uploadLatencyBoxPlot, values, {
                    width: Math.max(200, parentWidth - 16),
                    height: 60,
                    barColor: 'var(--color-upload)',
                    unit: 'ms'
                });
            }
        }
    }

    /**
     * Handle packet loss progress
     * Note: During the test, acks are still arriving so we only show progress counts,
     * not the loss percentage (which would be misleadingly high)
     */
    function handlePacketLossProgress(sent, total, received) {
        // Use main progress bar for packet loss test
        if (elements.progressFill) {
            const percent = (sent / total) * 100;
            elements.progressFill.style.width = `${percent}%`;
        }

        // Update main status text with packet loss progress
        if (elements.progressStatus) {
            elements.progressStatus.textContent = `Packet Loss Test (${sent}/${total})`;
        }

        // Show progress as sent/total in badge
        if (elements.packetLossBadge) {
            elements.packetLossBadge.textContent = `${sent}/${total}`;
        }

        // Don't show loss percentage during test - it's misleading since acks are still arriving
        if (elements.packetLossDetail) {
            elements.packetLossDetail.textContent = 'Testing...';
        }

        if (elements.packetsReceived) {
            elements.packetsReceived.textContent = `Sent ${sent} of ${total} packets`;
        }
    }

    /**
     * Handle test completion
     */
    function handleComplete(results, summary, quality) {
        state.summary = summary;
        state.quality = quality;
        state.packetLoss = results.packetLoss;
        state.networkScore = results.networkQualityScore;

        // Update final hero values
        updateHeroValue('download', summary.downloadMbps);
        updateHeroValue('upload', summary.uploadMbps);

        if (elements.latencyValue) {
            elements.latencyValue.textContent = summary.latencyUnloadedMs.toFixed(1);
        }

        if (elements.jitterValue) {
            elements.jitterValue.textContent = summary.jitterMs.toFixed(1);
        }

        if (elements.packetLossValue) {
            elements.packetLossValue.textContent = summary.packetLossPercent.toFixed(2);
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

        // Update new enhanced metrics displays
        if (results.lossPattern) {
            updateLossPatternDisplay(results.lossPattern);
        }

        updateDataChannelStatsDisplay(results.dataChannelStats);

        if (results.bandwidthEstimate) {
            updateBandwidthEstimationDisplay(results.bandwidthEstimate);
        }

        if (results.networkQualityScore) {
            updateNetworkQualityScoreDisplay(results.networkQualityScore);
        }

        if (results.testConfidence) {
            updateTestConfidenceDisplay(results.testConfidence);
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
     * Handle timing API warnings - show toast only after multiple fallbacks
     */
    function handleTimingWarning(type, message) {
        state.timingFallbackCount++;
        // Only show warning if we've had multiple fallbacks (not just initial timing hiccups)
        if (!state.timingWarningShown && state.timingFallbackCount >= 5) {
            state.timingWarningShown = true;
            showToast('Resource Timing API limited - some measurements using fallback timing', 5000);
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
            // Handle unknown/local network gracefully
            if (state.meta.asOrganization && state.meta.asOrganization !== 'Unknown' && state.meta.asn > 0) {
                elements.clientNetwork.textContent = `${state.meta.asOrganization} (AS${state.meta.asn})`;
            } else if (state.meta.asn > 0) {
                elements.clientNetwork.textContent = `AS${state.meta.asn}`;
            } else {
                elements.clientNetwork.textContent = 'Local Network';
            }
        }

        if (elements.clientIp) {
            elements.clientIp.textContent = state.meta.clientIp;
        }

        if (elements.connectionType) {
            const isIPv6 = state.meta.clientIp.includes(':');
            elements.connectionType.textContent = isIPv6 ? 'IPv6' : 'IPv4';
        }

        // Extended location display
        if (elements.clientLocation) {
            const parts = [];
            if (state.meta.city && state.meta.city !== 'Unknown') parts.push(state.meta.city);
            if (state.meta.region && state.meta.region !== 'Unknown') parts.push(state.meta.region);
            if (state.meta.country) parts.push(state.meta.country);
            elements.clientLocation.textContent = parts.length > 0 ? parts.join(', ') : 'Unknown';
        }

        if (elements.clientTimezone) {
            elements.clientTimezone.textContent = state.meta.timezone || 'Unknown';
        }

        // Calculate and display distance to server
        if (elements.serverDistance && serverLocation &&
            serverLocation.lat != null && serverLocation.lon != null &&
            state.meta.latitude && state.meta.longitude) {
            const distance = haversineDistance(
                state.meta.latitude, state.meta.longitude,
                serverLocation.lat, serverLocation.lon
            );
            elements.serverDistance.textContent = `${Math.round(distance)} km`;
        }

        // Only render map once (don't reset on each test start)
        if (elements.mapContainer && serverLocation && (serverLocation.lat != null && serverLocation.lon != null) && !state.mapRendered) {
            const clientLat = state.meta.latitude;
            const clientLon = state.meta.longitude;
            const hasClientLocation = (clientLat != null && clientLon != null) && (clientLat !== 0 || clientLon !== 0);

            if (hasClientLocation) {
                renderMapWithBothLocations(
                    serverLocation.lat, serverLocation.lon, serverLocation.city,
                    clientLat, clientLon
                );
            } else {
                renderMap(serverLocation.lat, serverLocation.lon, serverLocation.city);
            }
            state.mapRendered = true;
        }
    }

    /**
     * Render map showing server location only (using Leaflet)
     */
    function renderMap(lat, lon, label) {
        // Clear container and create map div
        elements.mapContainer.innerHTML = '<div id="leaflet-map" style="width:100%;height:100%"></div>';

        const map = L.map('leaflet-map', {
            zoomControl: false,
            attributionControl: false
        }).setView([lat, lon], 8);

        // Add tile layer (dark theme compatible)
        L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
            maxZoom: 19
        }).addTo(map);

        // Server marker (blue)
        const serverIcon = L.divIcon({
            className: 'map-marker server-marker',
            html: '<div class="marker-dot server"></div>',
            iconSize: [12, 12],
            iconAnchor: [6, 6]
        });

        L.marker([lat, lon], { icon: serverIcon })
            .bindTooltip(label, { permanent: false, direction: 'top' })
            .addTo(map);

        state.leafletMap = map;
    }

    /**
     * Render map showing both server and client locations (using Leaflet)
     */
    function renderMapWithBothLocations(serverLat, serverLon, serverLabel, clientLat, clientLon) {
        // Clear container and create map div
        elements.mapContainer.innerHTML = '<div id="leaflet-map" style="width:100%;height:100%"></div>';

        const map = L.map('leaflet-map', {
            zoomControl: false,
            attributionControl: false
        });

        // Add tile layer (dark theme compatible)
        L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
            maxZoom: 19
        }).addTo(map);

        // Server marker (blue)
        const serverIcon = L.divIcon({
            className: 'map-marker server-marker',
            html: '<div class="marker-dot server"></div>',
            iconSize: [12, 12],
            iconAnchor: [6, 6]
        });

        // Client marker (green)
        const clientIcon = L.divIcon({
            className: 'map-marker client-marker',
            html: '<div class="marker-dot client"></div>',
            iconSize: [12, 12],
            iconAnchor: [6, 6]
        });

        const serverMarker = L.marker([serverLat, serverLon], { icon: serverIcon })
            .bindTooltip(`Server: ${serverLabel}`, { permanent: false, direction: 'top' })
            .addTo(map);

        const clientMarker = L.marker([clientLat, clientLon], { icon: clientIcon })
            .bindTooltip('You', { permanent: false, direction: 'top' })
            .addTo(map);

        // Fit bounds to show both markers with padding
        const bounds = L.latLngBounds([
            [serverLat, serverLon],
            [clientLat, clientLon]
        ]);
        // maxZoom 8 keeps view at ~1000+ sq miles minimum (avoids too tight zoom)
        map.fitBounds(bounds, { padding: [30, 30], maxZoom: 8 });

        // Draw a line between them
        L.polyline([[clientLat, clientLon], [serverLat, serverLon]], {
            color: '#8b5cf6',
            weight: 2,
            opacity: 0.6,
            dashArray: '5, 10'
        }).addTo(map);

        state.leafletMap = map;
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
        const width = elements.downloadSparkline.clientWidth || 150;
        Charts.sparkline(elements.downloadSparkline, speeds, {
            width: width,
            height: 32,
            strokeColor: 'var(--color-download)',
            fillColor: 'var(--color-download)',
            fillOpacity: 0.15,
            strokeWidth: 1.5,
            dotRadius: 2
        });
    }

    /**
     * Update upload sparkline
     */
    function updateUploadSparkline() {
        if (!elements.uploadSparkline || state.uploadSamples.length < 2) return;

        const speeds = state.uploadSamples.map(s => s.mbps);
        const width = elements.uploadSparkline.clientWidth || 150;
        Charts.sparkline(elements.uploadSparkline, speeds, {
            width: width,
            height: 32,
            strokeColor: 'var(--color-upload)',
            fillColor: 'var(--color-upload)',
            fillOpacity: 0.15,
            strokeWidth: 1.5,
            dotRadius: 2
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

        // Get profile samples
        const profileSamples = (type === 'download' ? state.downloadSamples : state.uploadSamples)
            .filter(s => s.profile === profile)
            .map(s => s.mbps);

        // Update box plot - use card width minus padding, or fallback to 280
        const boxPlotContainer = card.querySelector('.test-box-plot');
        if (boxPlotContainer && profileSamples.length >= 2) {
            // Use the card's width for better responsiveness
            const cardWidth = card.offsetWidth || 320;
            const chartWidth = Math.max(200, cardWidth - 48); // 48px for padding
            const stats = Charts.boxPlot(boxPlotContainer, profileSamples, {
                width: chartWidth,
                height: 60,
                barColor: type === 'download' ? 'var(--color-download)' : 'var(--color-upload)',
                unit: 'Mbps'
            });

            // Update stats display
            const statsContainer = card.querySelector('.test-stats');
            if (statsContainer && stats) {
                statsContainer.innerHTML = `
                    <div class="stats-grid">
                        <span class="stat"><b>Min:</b> ${stats.min.toFixed(1)} Mbps</span>
                        <span class="stat"><b>Max:</b> ${stats.max.toFixed(1)} Mbps</span>
                        <span class="stat"><b>Avg:</b> ${stats.average.toFixed(1)} Mbps</span>
                        <span class="stat"><b>Median:</b> ${stats.median.toFixed(1)} Mbps</span>
                    </div>
                `;
            }
        }

        // Add to sparkline chart
        const chartContainer = card.querySelector('.test-chart');
        if (chartContainer && profileSamples.length > 0) {
            Charts.sparkline(chartContainer, profileSamples, {
                width: chartContainer.clientWidth || 150,
                height: 40,
                strokeColor: type === 'download' ? 'var(--color-download)' : 'var(--color-upload)'
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
            <div class="test-card-header">
                <div class="test-card-info">
                    <span class="test-card-title">${sizeLabel} ${type} test</span>
                    <span class="run-count">(0/${totalRuns})</span>
                </div>
                <span class="test-speed"><span class="placeholder"></span></span>
            </div>
            <div class="test-card-chart">
                <div class="test-box-plot" data-tooltip-target="boxplot"></div>
            </div>
            <div class="accordion-header">
                <span class="accordion-label">Details</span>
                <span class="accordion-icon"></span>
            </div>
            <div class="accordion-content">
                <div class="test-stats"></div>
                <div class="test-chart-label">Speed over time</div>
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

        const ph = '<span class="placeholder"></span>';

        // Video Streaming
        if (elements.streamingScore) {
            const grade = quality.videoStreaming;
            // Remove old grade classes and add new one
            elements.streamingScore.classList.remove('great', 'good', 'okay', 'poor');
            if (gradeClass[grade]) elements.streamingScore.classList.add(gradeClass[grade]);
            const text = elements.streamingScore.querySelector('.grade-text');
            if (text) text.innerHTML = grade || ph;
        }

        // Gaming
        if (elements.gamingScore) {
            const grade = quality.gaming;
            elements.gamingScore.classList.remove('great', 'good', 'okay', 'poor');
            if (gradeClass[grade]) elements.gamingScore.classList.add(gradeClass[grade]);
            const text = elements.gamingScore.querySelector('.grade-text');
            if (text) text.innerHTML = grade || ph;
        }

        // Video Chatting
        if (elements.videoChatScore) {
            const grade = quality.videoChatting;
            elements.videoChatScore.classList.remove('great', 'good', 'okay', 'poor');
            if (gradeClass[grade]) elements.videoChatScore.classList.add(gradeClass[grade]);
            const text = elements.videoChatScore.querySelector('.grade-text');
            if (text) text.innerHTML = grade || ph;
        }
    }

    /**
     * Update packet loss details
     */
    function updatePacketLossDetails(packetLoss) {
        // Handle unavailable state (WebRTC failed)
        if (packetLoss.unavailable) {
            if (elements.packetLossBadge) {
                elements.packetLossBadge.textContent = 'N/A';
            }
            if (elements.packetLossFill) {
                elements.packetLossFill.style.width = '0%';
            }
            if (elements.packetLossDetail) {
                elements.packetLossDetail.textContent = 'Unavailable';
            }
            if (elements.packetsReceived) {
                elements.packetsReceived.textContent = packetLoss.reason || 'Test unavailable';
            }
            const ph = '<span class="placeholder"></span>';
            if (elements.rttMin) elements.rttMin.innerHTML = ph;
            if (elements.rttMedian) elements.rttMedian.innerHTML = ph;
            if (elements.rttP90) elements.rttP90.innerHTML = ph;
            if (elements.rttJitter) elements.rttJitter.innerHTML = ph;
            return;
        }

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
            '100kB': '100 kB',
            '1MB': '1 MB',
            '10MB': '10 MB',
            '25MB': '25 MB',
            '50MB': '50 MB',
            '100MB': '100 MB'
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
     * Encode results into a compact URL-safe string
     * Format: {base}-{pkt}-{geo}-{dl}-{ul}-{latU}-{latD}-{latL}[-{server}]
     * All values base36, sections separated by - (URL-safe)
     */
    function encodeResultsForURL() {
        if (!state.summary) return null;

        // Pack quality grades: each 0-4, packed as qs*25 + qg*5 + qv
        let q = 0;
        if (state.quality) {
            q = encodeGrade(state.quality.videoStreaming) * 25 +
                encodeGrade(state.quality.gaming) * 5 +
                encodeGrade(state.quality.videoChatting);
        }

        // Base data: d.u.l.j.p.ns.t.q
        const base = [
            Math.round(state.summary.downloadMbps * 10),
            Math.round(state.summary.uploadMbps * 10),
            Math.round(state.summary.latencyUnloadedMs * 10),
            Math.round(state.summary.jitterMs * 10),
            Math.round(state.summary.packetLossPercent * 100),
            state.networkScore?.overall || 0,
            Math.floor(Date.now() / 1000),
            q
        ].map(v => v.toString(36)).join('.');

        // Packet loss stats: sent.recv.rttMin.rttMed.rttP90.rttJitter
        const pl = state.packetLoss;
        const pkt = [
            pl?.sent || 0,
            pl?.received || 0,
            Math.round((pl?.rttStatsMs?.min || 0) * 10),
            Math.round((pl?.rttStatsMs?.median || 0) * 10),
            Math.round((pl?.rttStatsMs?.p90 || 0) * 10),
            Math.round((pl?.jitterMs || 0) * 10)
        ].map(v => v.toString(36)).join('.');

        // Geo coords: clientLat.clientLon.serverLat.serverLon (scaled by 1000 for precision)
        const serverLoc = state.locations?.find(l => l.iata === state.meta?.colo);
        const geo = [
            Math.round((state.meta?.latitude || 0) * 1000),
            Math.round((state.meta?.longitude || 0) * 1000),
            Math.round((serverLoc?.lat || 0) * 1000),
            Math.round((serverLoc?.lon || 0) * 1000)
        ].map(v => encodeSignedInt(v)).join('.');

        // Sample arrays with delta encoding (max 8 points for speeds, 8 for latency)
        const dl = encodeSamplesDelta(state.downloadSamples.map(s => s.mbps), 8);
        const ul = encodeSamplesDelta(state.uploadSamples.map(s => s.mbps), 8);
        const latU = encodeSamplesDelta(state.latencySamples.filter(s => s.phase === 'unloaded').map(s => s.rttMs), 8);
        const latD = encodeSamplesDelta(state.latencySamples.filter(s => s.phase === 'download').map(s => s.rttMs), 5);
        const latL = encodeSamplesDelta(state.latencySamples.filter(s => s.phase === 'upload').map(s => s.rttMs), 5);

        // Build with - separator (URL-safe, no encoding needed)
        const parts = [base, pkt, geo, dl, ul, latU, latD, latL];

        // Add server city if available
        if (state.meta?.server?.city) {
            parts.push(encodeURIComponent(state.meta.server.city));
        }

        return parts.join('-');
    }

    /**
     * Encode signed integer to base36 (prefix 'n' for negative)
     */
    function encodeSignedInt(v) {
        if (v < 0) return 'n' + Math.abs(v).toString(36);
        return v.toString(36);
    }

    /**
     * Decode signed integer from base36
     */
    function decodeSignedInt(s) {
        if (s.startsWith('n')) return -parseInt(s.slice(1), 36);
        return parseInt(s, 36);
    }

    /**
     * Encode samples with delta compression
     * Format: first*10 in base36, then deltas separated by commas
     */
    function encodeSamplesDelta(values, maxPoints) {
        if (!values || values.length === 0) return '';

        // Simple downsample by picking evenly spaced points
        let samples = values;
        if (values.length > maxPoints) {
            samples = [];
            for (let i = 0; i < maxPoints; i++) {
                const idx = Math.floor(i * (values.length - 1) / (maxPoints - 1));
                samples.push(values[idx]);
            }
        }

        // Delta encode: first value absolute, rest as deltas
        const scaled = samples.map(v => Math.round(v * 10));
        const result = [scaled[0].toString(36)];
        for (let i = 1; i < scaled.length; i++) {
            const delta = scaled[i] - scaled[i - 1];
            result.push(encodeSignedInt(delta));
        }
        return result.join(',');
    }

    /**
     * Decode delta-encoded samples back to array
     */
    function decodeSamplesDelta(encoded) {
        if (!encoded || encoded === '') return [];
        const parts = encoded.split(',');
        const result = [parseInt(parts[0], 36) / 10];
        let prev = parseInt(parts[0], 36);
        for (let i = 1; i < parts.length; i++) {
            prev += decodeSignedInt(parts[i]);
            result.push(prev / 10);
        }
        return result;
    }

    /**
     * Decode results from URL parameter
     * Format: {base}-{pkt}-{geo}-{dl}-{ul}-{latU}-{latD}-{latL}[-{server}]
     */
    function decodeResultsFromURL(encoded) {
        try {
            const sections = encoded.split('-');
            if (sections.length < 8) return null;

            // Parse base data: d.u.l.j.p.ns.t.q
            const base = sections[0].split('.');
            if (base.length < 8) return null;

            const d = parseInt(base[0], 36) / 10;
            const u = parseInt(base[1], 36) / 10;
            const l = parseInt(base[2], 36) / 10;
            const j = parseInt(base[3], 36) / 10;
            const p = parseInt(base[4], 36) / 100;
            const ns = parseInt(base[5], 36);
            const t = parseInt(base[6], 36) * 1000;
            const q = parseInt(base[7], 36);

            const qs = Math.floor(q / 25);
            const qg = Math.floor((q % 25) / 5);
            const qv = q % 5;

            // Parse packet loss stats: sent.recv.rttMin.rttMed.rttP90.rttJitter
            const pkt = sections[1].split('.');
            const packetLoss = {
                sent: parseInt(pkt[0] || '0', 36),
                received: parseInt(pkt[1] || '0', 36),
                rttMin: parseInt(pkt[2] || '0', 36) / 10,
                rttMedian: parseInt(pkt[3] || '0', 36) / 10,
                rttP90: parseInt(pkt[4] || '0', 36) / 10,
                rttJitter: parseInt(pkt[5] || '0', 36) / 10
            };
            packetLoss.lossPercent = packetLoss.sent > 0
                ? ((packetLoss.sent - packetLoss.received) / packetLoss.sent) * 100
                : 0;

            // Parse geo coords: clientLat.clientLon.serverLat.serverLon
            const geo = sections[2].split('.');
            const coords = {
                clientLat: decodeSignedInt(geo[0] || '0') / 1000,
                clientLon: decodeSignedInt(geo[1] || '0') / 1000,
                serverLat: decodeSignedInt(geo[2] || '0') / 1000,
                serverLon: decodeSignedInt(geo[3] || '0') / 1000
            };

            // Decode delta-encoded sample arrays
            const downloadSamples = decodeSamplesDelta(sections[3]);
            const uploadSamples = decodeSamplesDelta(sections[4]);
            const latencyUnloaded = decodeSamplesDelta(sections[5]);
            const latencyDownload = decodeSamplesDelta(sections[6]);
            const latencyUpload = decodeSamplesDelta(sections[7]);

            // Server city (if present)
            let server = null;
            if (sections.length > 8) {
                try {
                    server = decodeURIComponent(sections.slice(8).join('-'));
                } catch (e) {
                    server = sections.slice(8).join('-');
                }
            }

            // Compute latency medians from samples
            const latencyDownloadMs = latencyDownload.length > 0 ? Charts.median(latencyDownload) : 0;
            const latencyUploadMs = latencyUpload.length > 0 ? Charts.median(latencyUpload) : 0;

            return {
                downloadMbps: d,
                uploadMbps: u,
                latencyMs: l,
                latencyDownloadMs: latencyDownloadMs,
                latencyUploadMs: latencyUploadMs,
                jitterMs: j,
                packetLossPercent: p,
                networkScore: ns,
                timestamp: t,
                server: server,
                packetLoss: packetLoss,
                coords: coords,
                quality: {
                    streaming: decodeGrade(qs),
                    gaming: decodeGrade(qg),
                    videoChatting: decodeGrade(qv)
                },
                downloadSamples: downloadSamples,
                uploadSamples: uploadSamples,
                latencyUnloaded: latencyUnloaded,
                latencyDownload: latencyDownload,
                latencyUpload: latencyUpload
            };
        } catch (e) {
            console.error('Failed to decode shared results:', e);
            return null;
        }
    }

    /**
     * Encode grade to numeric code (4=Great, 3=Good, 2=Okay, 1=Poor)
     */
    function encodeGrade(grade) {
        const codes = { 'Great': 4, 'Good': 3, 'Okay': 2, 'Poor': 1 };
        return codes[grade] || 0;
    }

    /**
     * Decode numeric grade code to full word
     */
    function decodeGrade(code) {
        const grades = { 4: 'Great', 3: 'Good', 2: 'Okay', 1: 'Poor' };
        return grades[code] || 'N/A';
    }

    /**
     * Check URL for shared results and display them
     */
    function checkForSharedResults() {
        const params = new URLSearchParams(window.location.search);
        const encoded = params.get('r');

        if (!encoded) {
            return false;
        }

        const results = decodeResultsFromURL(encoded);
        if (!results) {
            return false;
        }

        // Display shared results
        displaySharedResults(results);
        return true;
    }

    /**
     * Display shared results in read-only mode
     */
    function displaySharedResults(results) {
        // Update hero values
        if (elements.downloadValue) {
            elements.downloadValue.textContent = results.downloadMbps.toFixed(1);
        }
        if (elements.uploadValue) {
            elements.uploadValue.textContent = results.uploadMbps.toFixed(1);
        }
        if (elements.latencyValue) {
            elements.latencyValue.textContent = results.latencyMs.toFixed(1);
        }
        if (elements.jitterValue) {
            elements.jitterValue.textContent = results.jitterMs.toFixed(1);
        }
        if (elements.packetLossValue) {
            elements.packetLossValue.textContent = results.packetLossPercent.toFixed(2);
        }

        // Render sparklines if sample data available
        if (results.downloadSamples?.length >= 2 && elements.downloadSparkline) {
            const width = elements.downloadSparkline.clientWidth || 150;
            Charts.sparkline(elements.downloadSparkline, results.downloadSamples, {
                width, height: 32,
                strokeColor: 'var(--color-download)',
                fillColor: 'var(--color-download)',
                fillOpacity: 0.15, strokeWidth: 1.5, dotRadius: 2
            });
        }

        if (results.uploadSamples?.length >= 2 && elements.uploadSparkline) {
            const width = elements.uploadSparkline.clientWidth || 150;
            Charts.sparkline(elements.uploadSparkline, results.uploadSamples, {
                width, height: 32,
                strokeColor: 'var(--color-upload)',
                fillColor: 'var(--color-upload)',
                fillOpacity: 0.15, strokeWidth: 1.5, dotRadius: 2
            });
        }

        // Render unloaded latency box plot
        if (results.latencyUnloaded?.length >= 2) {
            if (elements.unloadedLatencyBoxPlot) {
                const w = elements.unloadedLatencyBoxPlot.parentElement?.offsetWidth || 300;
                Charts.boxPlot(elements.unloadedLatencyBoxPlot, results.latencyUnloaded, {
                    width: Math.max(200, w - 16), height: 60,
                    barColor: 'var(--color-latency)', unit: 'ms'
                });
            }
            const min = Math.min(...results.latencyUnloaded);
            const max = Math.max(...results.latencyUnloaded);
            const med = Charts.median(results.latencyUnloaded);
            if (elements.unloadedMin) elements.unloadedMin.textContent = `${min.toFixed(1)} ms`;
            if (elements.unloadedMedian) elements.unloadedMedian.textContent = `${med.toFixed(1)} ms`;
            if (elements.unloadedMax) elements.unloadedMax.textContent = `${max.toFixed(1)} ms`;
            if (elements.unloadedLatencyCount) elements.unloadedLatencyCount.textContent = results.latencyUnloaded.length;
            if (elements.unloadedLatencySummary) {
                elements.unloadedLatencySummary.textContent = `${min.toFixed(1)} - ${max.toFixed(1)} ms`;
            }
        }

        // Render download latency box plot
        if (results.latencyDownload?.length >= 2) {
            if (elements.downloadLatencyBoxPlot) {
                const w = elements.downloadLatencyBoxPlot.parentElement?.offsetWidth || 300;
                Charts.boxPlot(elements.downloadLatencyBoxPlot, results.latencyDownload, {
                    width: Math.max(200, w - 16), height: 60,
                    barColor: 'var(--color-download)', unit: 'ms'
                });
            }
            const min = Math.min(...results.latencyDownload);
            const max = Math.max(...results.latencyDownload);
            if (elements.downloadLatencyCount) elements.downloadLatencyCount.textContent = `${results.latencyDownload.length}/5`;
            if (elements.downloadLatencySummary) {
                elements.downloadLatencySummary.textContent = `${min.toFixed(1)} - ${max.toFixed(1)} ms`;
            }
        }

        // Render upload latency box plot
        if (results.latencyUpload?.length >= 2) {
            if (elements.uploadLatencyBoxPlot) {
                const w = elements.uploadLatencyBoxPlot.parentElement?.offsetWidth || 300;
                Charts.boxPlot(elements.uploadLatencyBoxPlot, results.latencyUpload, {
                    width: Math.max(200, w - 16), height: 60,
                    barColor: 'var(--color-upload)', unit: 'ms'
                });
            }
            const min = Math.min(...results.latencyUpload);
            const max = Math.max(...results.latencyUpload);
            if (elements.uploadLatencyCount) elements.uploadLatencyCount.textContent = `${results.latencyUpload.length}/5`;
            if (elements.uploadLatencySummary) {
                elements.uploadLatencySummary.textContent = `${min.toFixed(1)} - ${max.toFixed(1)} ms`;
            }
        }

        // Update packet loss card
        const pl = results.packetLoss;
        if (pl && pl.sent > 0) {
            if (elements.packetLossBadge) elements.packetLossBadge.textContent = `${pl.received}/${pl.sent}`;
            if (elements.packetLossFill) {
                const successPercent = (pl.received / pl.sent) * 100;
                elements.packetLossFill.style.width = `${successPercent}%`;
            }
            if (elements.packetLossDetail) elements.packetLossDetail.textContent = `${pl.lossPercent.toFixed(2)}%`;
            if (elements.packetsReceived) elements.packetsReceived.textContent = `${pl.received} / ${pl.sent} packets`;
            if (elements.rttMin) elements.rttMin.textContent = `${pl.rttMin.toFixed(1)} ms`;
            if (elements.rttMedian) elements.rttMedian.textContent = `${pl.rttMedian.toFixed(1)} ms`;
            if (elements.rttP90) elements.rttP90.textContent = `${pl.rttP90.toFixed(1)} ms`;
            if (elements.rttJitter) elements.rttJitter.textContent = `${pl.rttJitter.toFixed(1)} ms`;
        } else if (elements.packetLossDetail) {
            elements.packetLossDetail.textContent = `${results.packetLossPercent.toFixed(2)}%`;
        }

        // Render map if coordinates available
        const c = results.coords;
        if (c && elements.mapContainer && (c.serverLat !== 0 || c.serverLon !== 0)) {
            if (c.clientLat !== 0 || c.clientLon !== 0) {
                renderMapWithBothLocations(c.serverLat, c.serverLon, results.server || 'Server',
                                           c.clientLat, c.clientLon);
            } else {
                renderMap(c.serverLat, c.serverLon, results.server || 'Server');
            }
        }

        // Update network quality score
        if (results.networkScore > 0) {
            if (elements.overallScore) elements.overallScore.textContent = results.networkScore;
            if (elements.scoreGrade) {
                let grade = 'Poor';
                if (results.networkScore >= 90) grade = 'Excellent';
                else if (results.networkScore >= 75) grade = 'Good';
                else if (results.networkScore >= 50) grade = 'Fair';
                elements.scoreGrade.textContent = grade;
            }
        }

        // Update quality grades
        if (results.quality) {
            updateQualityScores({
                videoStreaming: results.quality.streaming,
                gaming: results.quality.gaming,
                videoChatting: results.quality.videoChatting
            });
        }

        // Update timestamp
        if (elements.measureTime) {
            const date = new Date(results.timestamp);
            elements.measureTime.textContent = `Shared result from ${formatTime(date)}`;
        }

        // Show server if available
        if (results.server && elements.serverLocation) {
            elements.serverLocation.textContent = results.server;
        }

        // Update UI to show this is a shared result
        if (elements.startButton) {
            elements.startButton.textContent = 'Run New Test';
        }

        showToast('Viewing shared speed test results', 3000);
    }

    /**
     * Share results with encoded URL
     */
    async function shareResults() {
        if (!state.summary) return;

        const encoded = encodeResultsForURL();
        if (!encoded) return;

        // Build share URL
        const baseUrl = window.location.origin + window.location.pathname;
        const shareUrl = `${baseUrl}?r=${encoded}`;

        const shareText = `Download: ${state.summary.downloadMbps.toFixed(1)} Mbps | ` +
                         `Upload: ${state.summary.uploadMbps.toFixed(1)} Mbps | ` +
                         `Latency: ${state.summary.latencyUnloadedMs.toFixed(1)} ms`;

        const shareData = {
            title: 'Speed Test Results',
            text: shareText,
            url: shareUrl
        };

        if (navigator.share) {
            try {
                await navigator.share(shareData);
            } catch (err) {
                if (err.name !== 'AbortError') {
                    copyToClipboard(shareUrl);
                }
            }
        } else {
            copyToClipboard(shareUrl);
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
     * Copy text to clipboard (with fallback for non-HTTPS)
     */
    function copyToClipboard(text) {
        // Modern clipboard API (requires HTTPS)
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).then(() => {
                showNotification('Results copied to clipboard');
            }).catch(() => {
                fallbackCopyToClipboard(text);
            });
        } else {
            fallbackCopyToClipboard(text);
        }
    }

    /**
     * Fallback clipboard copy using textarea + execCommand
     */
    function fallbackCopyToClipboard(text) {
        const textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.style.position = 'fixed';
        textarea.style.left = '-9999px';
        document.body.appendChild(textarea);
        textarea.select();
        try {
            document.execCommand('copy');
            showNotification('Results copied to clipboard');
        } catch (err) {
            showNotification('Failed to copy results', 'error');
        }
        document.body.removeChild(textarea);
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

    /**
     * Calculate haversine distance between two points (in km)
     */
    function haversineDistance(lat1, lon1, lat2, lon2) {
        const R = 6371; // Earth's radius in km
        const dLat = (lat2 - lat1) * Math.PI / 180;
        const dLon = (lon2 - lon1) * Math.PI / 180;
        const a = Math.sin(dLat/2) * Math.sin(dLat/2) +
                  Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) *
                  Math.sin(dLon/2) * Math.sin(dLon/2);
        const c = 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1-a));
        return R * c;
    }

    /**
     * Format bytes for display
     */
    function formatBytes(bytes) {
        if (bytes < 1000) return `${bytes} B`;
        if (bytes < 1000000) return `${(bytes / 1000).toFixed(1)} KB`;
        return `${(bytes / 1000000).toFixed(2)} MB`;
    }

    /**
     * Update loss pattern analysis display
     */
    function updateLossPatternDisplay(lossPattern) {
        if (!lossPattern) return;

        // Update badge
        if (elements.lossTypeBadge) {
            const typeLabels = {
                'none': 'No Loss',
                'random': 'Random',
                'burst': 'Burst',
                'tail': 'Tail'
            };
            elements.lossTypeBadge.textContent = typeLabels[lossPattern.type] || 'Unknown';
            elements.lossTypeBadge.className = `loss-type-badge ${lossPattern.type}`;
        }

        // Update timeline segments - transparent for no loss, color only for loss
        if (elements.lossTimeline) {
            const segments = elements.lossTimeline.querySelectorAll('.timeline-segment');
            const maxLoss = Math.max(...lossPattern.lossDistribution, 1);
            segments.forEach((seg, i) => {
                const loss = lossPattern.lossDistribution[i] || 0;
                const ratio = loss / maxLoss;
                seg.className = 'timeline-segment';
                if (loss === 0) {
                    seg.classList.add('loss-none');
                } else if (ratio < 0.5) {
                    seg.classList.add('loss-medium');
                } else {
                    seg.classList.add('loss-high');
                }
            });
        }

        // Update stats
        if (elements.burstCount) {
            elements.burstCount.textContent = lossPattern.burstCount > 0 ? lossPattern.burstCount : '-';
        }
        if (elements.maxBurst) {
            elements.maxBurst.textContent = lossPattern.maxBurstLength > 0 ? `${lossPattern.maxBurstLength} pkts` : '-';
        }
        if (elements.avgBurst) {
            elements.avgBurst.textContent = lossPattern.avgBurstLength > 0 ? `${lossPattern.avgBurstLength.toFixed(1)} pkts` : '-';
        }
    }

    /**
     * Update data channel stats display
     */
    function updateDataChannelStatsDisplay(stats) {

        if (!stats) {
            // Set placeholders for unavailable stats
            const ph = '<span class="placeholder"></span>';
            if (elements.webrtcConnectionBadge) elements.webrtcConnectionBadge.innerHTML = ph;
            if (elements.connectionPath) elements.connectionPath.innerHTML = ph;
            if (elements.webrtcProtocol) elements.webrtcProtocol.innerHTML = ph;
            if (elements.iceRtt) elements.iceRtt.innerHTML = ph;
            return;
        }

        // Connection type badge
        if (elements.webrtcConnectionBadge) {
            const typeLabels = {
                'host': 'Direct',
                'srflx': 'STUN',
                'prflx': 'Peer',
                'relay': 'Relay',
                'unknown': '-'
            };
            elements.webrtcConnectionBadge.textContent = typeLabels[stats.connectionType] || '-';
            elements.webrtcConnectionBadge.className = 'connection-type-badge';
            if (stats.connectionType === 'host') elements.webrtcConnectionBadge.classList.add('direct');
            else if (stats.connectionType === 'srflx' || stats.connectionType === 'prflx') elements.webrtcConnectionBadge.classList.add('stun');
            else if (stats.connectionType === 'relay') elements.webrtcConnectionBadge.classList.add('relay');
        }

        // Connection path (shorter labels)
        if (elements.connectionPath) {
            const pathLabels = {
                'host': 'Direct',
                'srflx': 'STUN NAT',
                'prflx': 'Peer reflexive',
                'relay': 'TURN relay'
            };
            elements.connectionPath.textContent = pathLabels[stats.connectionType] || '-';
        }

        if (elements.webrtcProtocol) {
            elements.webrtcProtocol.textContent = stats.protocol?.toUpperCase() || 'UDP';
        }

        if (elements.iceRtt) {
            elements.iceRtt.textContent = stats.currentRoundTripTime ? `${stats.currentRoundTripTime.toFixed(1)} ms` : '-';
        }
    }

    /**
     * Update bandwidth estimation display
     */
    function updateBandwidthEstimationDisplay(bandwidth) {
        if (!bandwidth) return;

        const trendArrows = {
            'stable': '→',
            'improving': '↑',
            'degrading': '↓'
        };

        // Download
        if (elements.downloadTrend) {
            elements.downloadTrend.textContent = `${trendArrows[bandwidth.downloadTrend] || '→'} ${bandwidth.downloadTrend}`;
            elements.downloadTrend.className = `bandwidth-trend ${bandwidth.downloadTrend}`;
        }
        if (elements.downloadPeak) {
            elements.downloadPeak.textContent = `${bandwidth.downloadPeakMbps.toFixed(1)} Mbps`;
        }
        if (elements.downloadSustained) {
            elements.downloadSustained.textContent = `${bandwidth.downloadSustainedMbps.toFixed(1)} Mbps`;
        }
        if (elements.downloadVariability) {
            elements.downloadVariability.textContent = `±${(bandwidth.downloadVariability * 100).toFixed(0)}%`;
        }

        // Upload
        if (elements.uploadTrend) {
            elements.uploadTrend.textContent = `${trendArrows[bandwidth.uploadTrend] || '→'} ${bandwidth.uploadTrend}`;
            elements.uploadTrend.className = `bandwidth-trend ${bandwidth.uploadTrend}`;
        }
        if (elements.uploadPeak) {
            elements.uploadPeak.textContent = `${bandwidth.uploadPeakMbps.toFixed(1)} Mbps`;
        }
        if (elements.uploadSustained) {
            elements.uploadSustained.textContent = `${bandwidth.uploadSustainedMbps.toFixed(1)} Mbps`;
        }
        if (elements.uploadVariability) {
            elements.uploadVariability.textContent = `±${(bandwidth.uploadVariability * 100).toFixed(0)}%`;
        }
    }

    /**
     * Update network quality score display
     */
    function updateNetworkQualityScoreDisplay(score) {
        if (!score) {
            return;
        }

        if (elements.overallScore) {
            elements.overallScore.textContent = score.overall;
        }

        if (elements.scoreGrade) {
            elements.scoreGrade.textContent = score.grade;
        }

        if (elements.scoreDescription) {
            elements.scoreDescription.textContent = score.description;
        }

        // Component bars
        const components = score.components;
        if (elements.bandwidthBar) elements.bandwidthBar.style.width = `${components.bandwidth}%`;
        if (elements.bandwidthScore) elements.bandwidthScore.textContent = components.bandwidth;

        if (elements.latencyBar) elements.latencyBar.style.width = `${components.latency}%`;
        if (elements.latencyScore) elements.latencyScore.textContent = components.latency;

        if (elements.stabilityBar) elements.stabilityBar.style.width = `${components.stability}%`;
        if (elements.stabilityScore) elements.stabilityScore.textContent = components.stability;

        if (elements.reliabilityBar) elements.reliabilityBar.style.width = `${components.reliability}%`;
        if (elements.reliabilityScore) elements.reliabilityScore.textContent = components.reliability;
    }

    /**
     * Update test confidence display
     */
    function updateTestConfidenceDisplay(confidence) {
        if (!confidence) return;

        // Update badge
        if (elements.confidenceBadge) {
            const labels = { 'high': 'High Confidence', 'medium': 'Medium Confidence', 'low': 'Low Confidence' };
            elements.confidenceBadge.textContent = labels[confidence.overall] || 'Unknown';
            elements.confidenceBadge.className = `confidence-badge ${confidence.overall}`;
        }

        const metrics = confidence.metrics;

        // Sample count
        if (elements.sampleCountIcon) {
            elements.sampleCountIcon.className = `confidence-icon ${metrics.sampleCount.adequate ? 'pass' : 'fail'}`;
        }
        if (elements.sampleCountDetail) {
            elements.sampleCountDetail.textContent = `DL: ${metrics.sampleCount.download}, UL: ${metrics.sampleCount.upload}, Lat: ${metrics.sampleCount.latency}`;
        }

        // Variability
        if (elements.variabilityIcon) {
            elements.variabilityIcon.className = `confidence-icon ${metrics.coefficientOfVariation.acceptable ? 'pass' : 'fail'}`;
        }
        if (elements.variabilityDetail) {
            elements.variabilityDetail.textContent = `DL: ±${metrics.coefficientOfVariation.download.toFixed(0)}%, UL: ±${metrics.coefficientOfVariation.upload.toFixed(0)}%`;
        }

        // Timing
        if (elements.timingIcon) {
            elements.timingIcon.className = `confidence-icon ${metrics.timingAccuracy.accurate ? 'pass' : 'fail'}`;
        }
        if (elements.timingDetail) {
            const timingText = metrics.timingAccuracy.resourceTimingUsed ? 'Resource Timing API' :
                              (metrics.timingAccuracy.fallbackCount > 0 ? `${metrics.timingAccuracy.fallbackCount} fallbacks` : 'Available');
            elements.timingDetail.textContent = timingText;
        }

        // Connection
        if (elements.connectionIcon) {
            elements.connectionIcon.className = `confidence-icon ${metrics.connectionStability.stable ? 'pass' : 'fail'}`;
        }
        if (elements.connectionDetail) {
            elements.connectionDetail.textContent = metrics.connectionStability.packetTestCompleted ? 'Stable' : 'Incomplete';
        }

        // Warnings
        if (elements.confidenceWarnings) {
            if (confidence.warnings.length > 0) {
                elements.confidenceWarnings.innerHTML = confidence.warnings
                    .map(w => `<div class="confidence-warning">${w}</div>`)
                    .join('');
            } else {
                elements.confidenceWarnings.innerHTML = '';
            }
        }
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
