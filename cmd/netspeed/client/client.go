// Package client provides the speed test client implementation.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yellowman/netspeed/internal/measurement"
	"github.com/yellowman/netspeed/internal/measurementhttp"
	"github.com/yellowman/netspeed/internal/protocol"
)

// timingInfo captures precise HTTP timing events.
type timingInfo struct {
	wroteRequest     time.Time
	gotFirstByte     time.Time
	gotConnection    bool
	connectionReused bool
}

// createTrace creates an httptrace.ClientTrace for precise timing.
func createTrace(t *timingInfo) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			t.gotConnection = true
			t.connectionReused = info.Reused
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			t.wroteRequest = time.Now()
		},
		GotFirstResponseByte: func() {
			t.gotFirstByte = time.Now()
		},
	}
}

// Config holds client configuration.
type Config struct {
	ServerURL          string
	Timeout            time.Duration
	Quick              bool
	DownloadOnly       bool
	UploadOnly         bool
	SkipPacketLoss     bool
	AccessToken        string
	DownloadPayload    string
	DownloadFraming    string
	DownloadChunkBytes int
	DownloadFlush      string
	OnProgress         func(stage string, current, total int, value float64)
}

// Buffer sizes for high-speed connections
const (
	ReadBufferSize            = 4 * 1024 * 1024 // 4MB read buffer
	WriteBufferSize           = 4 * 1024 * 1024 // 4MB write buffer
	legacyMaxTransferBytes    = 100_000_000     // conservative cap for older /meta responses
	maxMeasurementErrorBytes  = 4 * 1024
	maxMetaBodyBytes          = 1 * 1024 * 1024
	maxUploadReceiptBodyBytes = 64 * 1024
	maxPacketReportBodyBytes  = 64 * 1024
)

// UserAgent matches python-requests default format
const UserAgent = "python-requests/2.32.0"

// setRequestHeaders adds measurement headers and optional shared-token auth.
func (c *Client) setRequestHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")
	if c.cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	}
}

// Latency adaptation constants shared with the browser implementation.
const (
	LowLatencyThreshold     = 50 * time.Millisecond
	HighLatencyThreshold    = 100 * time.Millisecond
	MinBandwidthForParallel = 2.0 // Mbps
)

// Client performs speed tests.
type Client struct {
	cfg                             Config
	httpClient                      *http.Client
	maxTransferBytes                int64
	uploadReceiptVersion            int
	packetLossFrameVersion          int
	maxConcurrentTransfersPerClient int
	measurementTransport            measurementhttp.Selection
	websocketLatency                *websocketLatencyProbe
}

func (c *Client) closeWebSocketLatency() {
	if c.websocketLatency != nil {
		c.websocketLatency.close()
		c.websocketLatency = nil
	}
}

// New creates a new speed test client.
func New(cfg Config) *Client {
	// Custom dialer with TCP optimizations
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return &Client{
		cfg:                             cfg,
		maxTransferBytes:                legacyMaxTransferBytes,
		maxConcurrentTransfersPerClient: 24,
		measurementTransport:            measurementhttp.LegacySelection(),
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					conn, err := dialer.DialContext(ctx, network, addr)
					if err != nil {
						return nil, err
					}

					// Set TCP options for high-speed transfers
					if tcpConn, ok := conn.(*net.TCPConn); ok {
						tcpConn.SetNoDelay(true)                // Disable Nagle's algorithm
						tcpConn.SetReadBuffer(ReadBufferSize)   // 4MB read buffer
						tcpConn.SetWriteBuffer(WriteBufferSize) // 4MB write buffer
					}

					return conn, nil
				},
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   100,
				MaxConnsPerHost:       100,
				IdleConnTimeout:       90 * time.Second,
				DisableCompression:    true,  // Important for accurate bandwidth measurement
				ForceAttemptHTTP2:     false, // Use HTTP/1.1 like python-requests
				ReadBufferSize:        ReadBufferSize,
				WriteBufferSize:       WriteBufferSize,
				ResponseHeaderTimeout: 30 * time.Second,
			},
			Timeout: 120 * time.Second, // Allow for large transfers
		},
	}
}

// Run executes the full speed test suite.
func (c *Client) Run(ctx context.Context) (*Results, error) {
	_netspeedProgress := nsBeginProgress("speed test")
	defer _netspeedProgress.Done("complete")
	if c.cfg.DownloadOnly && c.cfg.UploadOnly {
		return nil, fmt.Errorf("download-only and upload-only modes are mutually exclusive")
	}

	results := &Results{
		Timestamp: time.Now(),
		StartTime: time.Now(),
		ServerURL: c.cfg.ServerURL,
	}

	// Fetch metadata
	meta, err := c.fetchMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	results.Meta = meta
	selection, err := measurementhttp.Negotiate(meta.MeasurementCapabilities, measurementhttp.Preferences{
		DownloadPayload:    c.cfg.DownloadPayload,
		DownloadFraming:    c.cfg.DownloadFraming,
		DownloadChunkBytes: c.cfg.DownloadChunkBytes,
		DownloadFlush:      c.cfg.DownloadFlush,
	})
	if err != nil {
		return nil, fmt.Errorf("negotiate HTTP measurement transport: %w", err)
	}
	c.measurementTransport = selection
	c.closeWebSocketLatency()
	c.websocketLatency = newWebSocketLatencyProbe(c.cfg, selection)
	defer c.closeWebSocketLatency()
	meta.MeasurementSelection = &c.measurementTransport
	if meta.MaxTransferBytes > 0 {
		c.maxTransferBytes = meta.MaxTransferBytes
	}
	if meta.MaxConcurrentTransfersPerClient > 0 {
		c.maxConcurrentTransfersPerClient = meta.MaxConcurrentTransfersPerClient
	}
	if c.maxConcurrentTransfersPerClient < 2 {
		return nil, fmt.Errorf("server per-client transfer limit %d is too low for concurrent loaded-latency measurement; need at least 2", c.maxConcurrentTransfersPerClient)
	}
	c.uploadReceiptVersion = meta.UploadReceiptVersion
	c.packetLossFrameVersion = meta.PacketLossFrameVersion
	if meta.MeasurementProtocolVersion < protocol.MeasurementProtocolVersion {
		return nil, fmt.Errorf("server measurement protocol %d is too old; need version %d",
			meta.MeasurementProtocolVersion, protocol.MeasurementProtocolVersion)
	}
	if !c.cfg.DownloadOnly && c.uploadReceiptVersion < protocol.UploadReceiptVersion {
		return nil, fmt.Errorf("server does not support verified upload receipts (need version %d)", protocol.UploadReceiptVersion)
	}

	// Run latency tests (unloaded) with adaptive batching
	latencySamples, err := c.runAdaptiveLatencyTest(ctx, "unloaded", 20)
	if err != nil {
		return nil, fmt.Errorf("latency test failed: %w", err)
	}
	results.LatencySamples = append(results.LatencySamples, latencySamples...)

	// Run bounded fixed-duration download windows
	if !c.cfg.UploadOnly {
		downloadSamples, loadedLatency, err := c.runDownloadTests(ctx)
		if err != nil {
			return nil, fmt.Errorf("download test failed: %w", err)
		}
		results.ThroughputSamples = append(results.ThroughputSamples, downloadSamples...)
		results.LatencySamples = append(results.LatencySamples, loadedLatency...)
	}

	// Run bounded fixed-duration upload windows
	if !c.cfg.DownloadOnly {
		uploadSamples, loadedLatency, err := c.runUploadTests(ctx)
		if err != nil {
			return nil, fmt.Errorf("upload test failed: %w", err)
		}
		results.ThroughputSamples = append(results.ThroughputSamples, uploadSamples...)
		results.LatencySamples = append(results.LatencySamples, loadedLatency...)
	}

	// Run packet loss test
	if !c.cfg.SkipPacketLoss {
		packetLoss, err := c.runPacketLossTest(ctx)
		if err != nil {
			// Packet loss test failure is not fatal
			results.PacketLoss = &PacketLossResult{
				Unavailable: true,
				Reason:      err.Error(),
			}
		} else {
			results.PacketLoss = packetLoss
		}
	}

	// Calculate summary
	results.Summary = c.calculateSummary(results)
	results.Quality = c.calculateQuality(results.Summary)
	results.EndTime = time.Now()
	results.TestConfidence = c.assessTestConfidence(results)

	return results, nil
}

// fetchMeta fetches server and client metadata.
func (c *Client) fetchMeta(ctx context.Context) (*Meta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.ServerURL+"/meta", nil)
	if err != nil {
		return nil, err
	}
	c.setRequestHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return nil, err
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return nil, fmt.Errorf("unexpected metadata content type %q", contentType)
	}

	var meta Meta
	if err := decodeLimitedJSON(resp.Body, maxMetaBodyBytes, &meta); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}

	return &meta, nil
}

// runAdaptiveLatencyTest runs latency probes with adaptive batching (matching web client).
func (c *Client) runAdaptiveLatencyTest(ctx context.Context, condition string, count int) ([]LatencySample, error) {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	if c.cfg.Quick {
		count = 5
	}

	samples := make([]LatencySample, 0, count)

	// Run the first probes sequentially to estimate connection quality.
	initialProbes := 3
	if count < initialProbes {
		initialProbes = count
	}

	for i := 0; i < initialProbes; i++ {
		select {
		case <-ctx.Done():
			return samples, ctx.Err()
		default:
		}

		sample, err := c.measureLatencySample(ctx, condition, i)
		if err != nil {
			continue
		}
		samples = append(samples, sample)

		if c.cfg.OnProgress != nil {
			c.cfg.OnProgress("latency", i+1, count, float64(sample.RTT.Microseconds())/1000)
		}
	}

	if len(samples) == 0 {
		return samples, fmt.Errorf("%s latency test produced no valid samples", condition)
	}

	// Choose the batching strategy from the observed median RTT.
	medianRTT := c.calculateMedianRTT(samples)
	useParallel := false

	if medianRTT < LowLatencyThreshold {
		useParallel = true
	} else if medianRTT >= HighLatencyThreshold {
		// High latency: check bandwidth to distinguish satellite from slow DSL
		bandwidth := c.quickBandwidthEstimate(ctx)
		useParallel = bandwidth >= MinBandwidthForParallel
	} else {
		useParallel = true // 50-100ms range
	}

	// Run the remaining probes with the selected batching strategy.
	remaining := count - len(samples)
	if useParallel {
		// Respect the server-advertised per-client transfer ceiling.
		batchSize := 5
		if c.maxConcurrentTransfersPerClient < batchSize {
			batchSize = c.maxConcurrentTransfersPerClient
		}
		for i := 0; i < remaining; i += batchSize {
			batch := batchSize
			if i+batch > remaining {
				batch = remaining - i
			}

			batchSamples := c.runParallelLatencyProbes(ctx, condition, len(samples), batch)
			samples = append(samples, batchSamples...)

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("latency", len(samples), count, float64(medianRTT.Milliseconds()))
			}
		}
	} else {
		// Sequential probes
		for i := len(samples); i < count; i++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureLatencySample(ctx, condition, i)
			if err != nil {
				continue
			}
			samples = append(samples, sample)

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("latency", i+1, count, float64(sample.RTT.Microseconds())/1000)
			}
		}
	}

	if len(samples) < requiredSuccessfulRuns(count) {
		return samples, fmt.Errorf("%s latency test produced %d/%d valid samples", condition, len(samples), count)
	}
	return samples, nil
}

// runParallelLatencyProbes runs multiple latency probes in parallel.
func (c *Client) runParallelLatencyProbes(ctx context.Context, condition string, startSeq, count int) []LatencySample {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	var wg sync.WaitGroup
	results := make(chan LatencySample, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			sample, err := c.measureLatencySample(ctx, condition, seq)
			if err != nil {
				return
			}
			results <- sample
		}(startSeq + i)
	}

	wg.Wait()
	close(results)

	var samples []LatencySample
	for sample := range results {
		samples = append(samples, sample)
	}
	return samples
}

// calculateMedianRTT calculates the median RTT from samples.
func (c *Client) calculateMedianRTT(samples []LatencySample) time.Duration {
	if len(samples) == 0 {
		return 0
	}

	rtts := make([]time.Duration, len(samples))
	for i, s := range samples {
		rtts[i] = s.RTT
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })

	return rtts[len(rtts)/2]
}

// quickBandwidthEstimate does a verified 100KB download to estimate bandwidth.
func (c *Client) quickBandwidthEstimate(ctx context.Context) float64 {
	const estimateBytes = int64(100_000)
	if c.maxTransferBytes < estimateBytes {
		return 0
	}
	sample, err := c.measureDownload(ctx, "estimate", estimateBytes, 0)
	if err != nil {
		return 0
	}
	return sample.Mbps
}

type latencyProbeMeasurement struct {
	RTT               time.Duration
	ConnectionReused  bool
	Transport         string
	TimingSource      string
	Method            string
	Path              string
	FallbackReason    string
	WebSocketProtocol string
}

func (c *Client) measureLatencySample(ctx context.Context, condition string, seq int) (LatencySample, error) {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	startedAt := time.Now()
	probe, err := c.measureLatency(ctx, condition, seq)
	endedAt := time.Now()
	if err != nil {
		return LatencySample{}, err
	}
	return LatencySample{
		Timestamp:           endedAt,
		StartedAt:           startedAt,
		EndedAt:             endedAt,
		RTT:                 probe.RTT,
		Condition:           condition,
		TimingSource:        probe.TimingSource,
		ConnectionReused:    probe.ConnectionReused,
		ProbeTransport:      probe.Transport,
		ProbeMethod:         probe.Method,
		ProbePath:           probe.Path,
		ProbeFallbackReason: probe.FallbackReason,
		WebSocketProtocol:   probe.WebSocketProtocol,
	}, nil
}

// measureLatency uses a dedicated zero-byte endpoint when advertised and falls
// back to /__down?bytes=0 for older servers. A server that advertises warm
// connection support must produce a reused keep-alive connection; an unmeasured
// cold probe is retried rather than contaminating the reported sample.
func (c *Client) measureLatency(ctx context.Context, condition string, seq int) (latencyProbeMeasurement, error) {
	fallbackReason := ""
	if c.websocketLatency != nil {
		if probe, used, reason := c.websocketLatency.measure(ctx, seq); used {
			return probe, nil
		} else {
			fallbackReason = reason
		}
	}
	probe, err := c.measureHTTPLatency(ctx, condition, seq)
	if err != nil {
		return latencyProbeMeasurement{}, err
	}
	probe.FallbackReason = fallbackReason
	return probe, nil
}

func (c *Client) measureHTTPLatency(ctx context.Context, condition string, seq int) (latencyProbeMeasurement, error) {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	attempts := 1
	if c.measurementTransport.WarmConnectionPing {
		attempts = 3
	}
	var last latencyProbeMeasurement
	for attempt := 0; attempt < attempts; attempt++ {
		probe, err := c.measureLatencyAttempt(ctx, condition, seq, attempt)
		if err != nil {
			return latencyProbeMeasurement{}, err
		}
		last = probe
		if !c.measurementTransport.WarmConnectionPing || probe.ConnectionReused {
			return probe, nil
		}
	}
	return latencyProbeMeasurement{}, fmt.Errorf("server advertised warm connection latency but %s %s was not reused after %d probes", last.Method, last.Path, attempts)
}

func (c *Client) measureLatencyAttempt(ctx context.Context, condition string, seq, attempt int) (latencyProbeMeasurement, error) {
	measID := fmt.Sprintf("%d-%s-%d-%d", time.Now().UnixNano(), condition, seq, attempt)
	method, requestURL := c.latencyMeasurementRequest(url.Values{
		"measId": {measID},
		"during": {condition},
		"seq":    {fmt.Sprintf("%d", seq)},
	})

	var timing timingInfo
	traceContext := httptrace.WithClientTrace(ctx, createTrace(&timing))
	req, err := http.NewRequestWithContext(traceContext, method, requestURL, nil)
	if err != nil {
		return latencyProbeMeasurement{}, err
	}
	c.setMeasurementRequestHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return latencyProbeMeasurement{}, err
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return latencyProbeMeasurement{}, err
	}
	if c.measurementTransport.LatencyUsesDownload {
		if err := c.verifyDownloadMeasurementResponse(resp, 0, "latency"); err != nil {
			return latencyProbeMeasurement{}, err
		}
	} else if err := c.verifyDedicatedLatencyResponse(resp); err != nil {
		return latencyProbeMeasurement{}, err
	}
	received, err := consumeExactBody(resp.Body, 0)
	if err != nil {
		return latencyProbeMeasurement{}, fmt.Errorf("read latency response: %w", err)
	}
	if received != 0 {
		return latencyProbeMeasurement{}, fmt.Errorf("latency response contained %d bytes; expected 0", received)
	}

	if !timing.gotConnection || timing.wroteRequest.IsZero() || timing.gotFirstByte.IsZero() {
		return latencyProbeMeasurement{}, fmt.Errorf("latency timing trace failed")
	}
	rtt := timing.gotFirstByte.Sub(timing.wroteRequest)
	if rtt <= 0 {
		return latencyProbeMeasurement{}, fmt.Errorf("invalid latency duration %s", rtt)
	}
	return latencyProbeMeasurement{
		RTT:              rtt,
		ConnectionReused: timing.connectionReused,
		Transport:        "http",
		TimingSource:     "httptrace",
		Method:           method,
		Path:             c.measurementTransport.LatencyPath,
	}, nil
}

// Profile configuration used only for the small baseline estimate. Sustained
// throughput is measured by fixed-duration windows below, not giant profiles.
type profile struct {
	Name  string
	Bytes int64
	Runs  int
}

var baselineDownloadProfiles = []profile{
	{Name: "100kB", Bytes: 100_000, Runs: 3},
	{Name: "1MB", Bytes: 1_000_000, Runs: 3},
}

var baselineUploadProfiles = []profile{
	{Name: "100kB", Bytes: 100_000, Runs: 3},
	{Name: "1MB", Bytes: 1_000_000, Runs: 3},
}

const (
	minWindowChunkBytes   = int64(100_000)
	maxWindowChunkBytes   = int64(256 * 1024 * 1024)
	targetRequestDuration = 250 * time.Millisecond
	fullWindowDuration    = 1500 * time.Millisecond
	quickWindowDuration   = 1 * time.Second
)

type windowPlan struct {
	ChunkBytes       int64
	Concurrency      int
	WindowDuration   time.Duration
	Windows          int
	LoadedWindow     int
	LoadedProbeCount int
}

func requiredSuccessfulRuns(total int) int {
	_netspeedProgress := nsBeginProgress("speed test")
	defer _netspeedProgress.Done("complete")
	if total <= 1 {
		return total
	}
	return (total + 1) / 2
}

func profilesWithinLimit(profiles []profile, maxBytes int64) []profile {
	filtered := make([]profile, 0, len(profiles))
	for _, candidate := range profiles {
		if candidate.Bytes <= maxBytes {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

// selectWindowPlan maps the baseline estimate to bounded request chunks and
// parallel flows. Increasing link rates increase concurrency, never a single
// allocation or request beyond maxWindowChunkBytes.
func selectWindowPlan(estimatedMbps float64, maxBytes int64, quick bool) windowPlan {
	_netspeedProgress := nsBeginProgress("sustained measurement window")
	defer _netspeedProgress.Done("complete")
	if estimatedMbps <= 0 || math.IsNaN(estimatedMbps) || math.IsInf(estimatedMbps, 0) {
		estimatedMbps = 10
	}

	concurrency := 1
	switch {
	case estimatedMbps >= 10_000:
		concurrency = 16
	case estimatedMbps >= 2_000:
		concurrency = 8
	case estimatedMbps >= 500:
		concurrency = 4
	case estimatedMbps >= 100:
		concurrency = 2
	}

	target := estimatedMbps * 1e6 / 8 * targetRequestDuration.Seconds() / float64(concurrency)
	chunkBytes := int64(math.Ceil(target/65536.0) * 65536)
	if chunkBytes < minWindowChunkBytes {
		chunkBytes = minWindowChunkBytes
	}
	if chunkBytes > maxWindowChunkBytes {
		chunkBytes = maxWindowChunkBytes
	}
	if maxBytes > 0 && chunkBytes > maxBytes {
		chunkBytes = maxBytes
	}

	plan := windowPlan{
		ChunkBytes:       chunkBytes,
		Concurrency:      concurrency,
		WindowDuration:   fullWindowDuration,
		Windows:          3,
		LoadedWindow:     1,
		LoadedProbeCount: 5,
	}
	if quick {
		plan.WindowDuration = quickWindowDuration
		plan.Windows = 1
		plan.LoadedWindow = 0
		plan.LoadedProbeCount = 3
	}
	return plan
}

// loadActivity records whether at least one measured transfer is active and
// increments gapGeneration whenever the aggregate load falls to zero. A probe
// is accepted only if active remained nonzero and the generation did not
// change from probe start through probe end.
type loadActivity struct {
	mu            sync.Mutex
	active        int
	gapGeneration uint64
}

type loadActivitySnapshot struct {
	active        int
	gapGeneration uint64
}

func (activity *loadActivity) begin() {
	if activity == nil {
		return
	}
	activity.mu.Lock()
	activity.active++
	activity.mu.Unlock()
}

func (activity *loadActivity) end() {
	if activity == nil {
		return
	}
	activity.mu.Lock()
	if activity.active > 0 {
		activity.active--
		if activity.active == 0 {
			activity.gapGeneration++
		}
	}
	activity.mu.Unlock()
}

func (activity *loadActivity) snapshot() loadActivitySnapshot {
	activity.mu.Lock()
	defer activity.mu.Unlock()
	return loadActivitySnapshot{active: activity.active, gapGeneration: activity.gapGeneration}
}

func (activity *loadActivity) waitActive(ctx context.Context, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()

	for {
		if activity.snapshot().active > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for sustained load")
		case <-ticker.C:
		}
	}
}

func medianProfileSpeed(samples []ThroughputSample, profileName string) float64 {
	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.Profile == profileName {
			values = append(values, sample.Mbps)
		}
	}
	if len(values) == 0 {
		return 10
	}
	return measurement.Percentile(values, 50)
}

func (c *Client) clampLoadConcurrency(requested int) int {
	maximum := c.maxConcurrentTransfersPerClient - 1 // reserve one slot for the loaded-latency probe
	if maximum < 1 {
		maximum = 1
	}
	if requested > maximum {
		return maximum
	}
	return requested
}

// runDownloadTests runs small verified baselines, then bounded fixed-duration
// windows. The selected window owns the loaded-latency probes.
func (c *Client) runDownloadTests(ctx context.Context) ([]ThroughputSample, []LatencySample, error) {
	_netspeedProgress := nsBeginProgress("download measurement")
	defer _netspeedProgress.Done("complete")
	profiles := profilesWithinLimit(baselineDownloadProfiles, c.maxTransferBytes)
	if len(profiles) != len(baselineDownloadProfiles) {
		return nil, nil, fmt.Errorf("server transfer limit %d is below the 1MB download baseline", c.maxTransferBytes)
	}
	baseline, err := c.runBaselineProfiles(ctx, profiles, "download")
	if err != nil {
		return nil, nil, err
	}
	plan := selectWindowPlan(medianProfileSpeed(baseline, "1MB"), c.maxTransferBytes, c.cfg.Quick)
	plan.Concurrency = c.clampLoadConcurrency(plan.Concurrency)
	windows, loaded, err := c.runSustainedWindows(ctx, "download", plan)
	if err != nil {
		return nil, nil, err
	}
	return append(baseline, windows...), loaded, nil
}

// runUploadTests is the upload counterpart to runDownloadTests.
func (c *Client) runUploadTests(ctx context.Context) ([]ThroughputSample, []LatencySample, error) {
	_netspeedProgress := nsBeginProgress("upload measurement")
	defer _netspeedProgress.Done("complete")
	profiles := profilesWithinLimit(baselineUploadProfiles, c.maxTransferBytes)
	if len(profiles) != len(baselineUploadProfiles) {
		return nil, nil, fmt.Errorf("server transfer limit %d is below the 1MB upload baseline", c.maxTransferBytes)
	}
	baseline, err := c.runBaselineProfiles(ctx, profiles, "upload")
	if err != nil {
		return nil, nil, err
	}
	plan := selectWindowPlan(medianProfileSpeed(baseline, "1MB"), c.maxTransferBytes, c.cfg.Quick)
	plan.Concurrency = c.clampLoadConcurrency(plan.Concurrency)
	windows, loaded, err := c.runSustainedWindows(ctx, "upload", plan)
	if err != nil {
		return nil, nil, err
	}
	return append(baseline, windows...), loaded, nil
}

func (c *Client) runBaselineProfiles(ctx context.Context, profiles []profile, direction string) ([]ThroughputSample, error) {
	_netspeedProgress := nsBeginProgress("calibration transfer")
	defer _netspeedProgress.Done("complete")
	var samples []ThroughputSample
	totalRuns := 0
	for _, candidate := range profiles {
		totalRuns += candidate.Runs
	}
	completed := 0

	for _, candidate := range profiles {
		valid := make([]ThroughputSample, 0, candidate.Runs)
		var lastErr error
		for run := 0; run < candidate.Runs; run++ {
			var sample ThroughputSample
			var err error
			if direction == "download" {
				sample, err = c.measureDownload(ctx, candidate.Name, candidate.Bytes, run)
			} else {
				sample, err = c.measureUpload(ctx, candidate.Name, candidate.Bytes, run)
			}
			if err != nil {
				lastErr = err
				continue
			}
			sample.SampleKind = "baseline"
			valid = append(valid, sample)
			completed++
			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress(direction, completed, totalRuns, sample.Mbps)
			}
		}
		if len(valid) < requiredSuccessfulRuns(candidate.Runs) {
			if lastErr == nil {
				lastErr = fmt.Errorf("measurement canceled or incomplete")
			}
			return samples, fmt.Errorf("%s baseline %s produced %d/%d valid samples: %w",
				direction, candidate.Name, len(valid), candidate.Runs, lastErr)
		}
		samples = append(samples, valid...)
	}
	return samples, nil
}

func (c *Client) runSustainedWindows(ctx context.Context, direction string, plan windowPlan) ([]ThroughputSample, []LatencySample, error) {
	_netspeedProgress := nsBeginProgress("sustained measurement window")
	defer _netspeedProgress.Done("complete")
	if plan.ChunkBytes <= 0 || plan.Concurrency <= 0 || plan.Windows <= 0 {
		return nil, nil, fmt.Errorf("invalid %s sustained-window plan: %#v", direction, plan)
	}
	windows := make([]ThroughputSample, 0, plan.Windows)
	var loaded []LatencySample
	for windowIndex := 0; windowIndex < plan.Windows; windowIndex++ {
		withLoadedLatency := windowIndex == plan.LoadedWindow
		sample, probes, err := c.runThroughputWindow(ctx, direction, plan, windowIndex, withLoadedLatency)
		if err != nil {
			return windows, loaded, err
		}
		windows = append(windows, sample)
		loaded = append(loaded, probes...)
		if c.cfg.OnProgress != nil {
			c.cfg.OnProgress(direction, windowIndex+1, plan.Windows, sample.Mbps)
		}
	}
	return windows, loaded, nil
}

type windowAggregate struct {
	mu       sync.Mutex
	bytes    int64
	requests int
	lastErr  error
}

func (aggregate *windowAggregate) success(sample ThroughputSample) {
	aggregate.mu.Lock()
	aggregate.bytes += sample.SizeBytes
	aggregate.requests++
	aggregate.mu.Unlock()
}

func (aggregate *windowAggregate) failure(err error) {
	aggregate.mu.Lock()
	aggregate.lastErr = err
	aggregate.mu.Unlock()
}

func (aggregate *windowAggregate) snapshot() (int64, int, error) {
	aggregate.mu.Lock()
	defer aggregate.mu.Unlock()
	return aggregate.bytes, aggregate.requests, aggregate.lastErr
}

func (c *Client) runThroughputWindow(
	ctx context.Context,
	direction string,
	plan windowPlan,
	windowIndex int,
	withLoadedLatency bool,
) (ThroughputSample, []LatencySample, error) {
	_netspeedProgress := nsBeginProgress("sustained measurement window")
	defer _netspeedProgress.Done("complete")
	activity := &loadActivity{}
	aggregate := &windowAggregate{}
	startGate := make(chan struct{})
	stop := make(chan struct{})
	var workers sync.WaitGroup

	for workerIndex := 0; workerIndex < plan.Concurrency; workerIndex++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-startGate
			requestIndex := 0
			for {
				select {
				case <-ctx.Done():
					return
				case <-stop:
					return
				default:
				}

				profileName := fmt.Sprintf("window-%d", windowIndex+1)
				runIndex := worker*1_000_000 + requestIndex
				requestIndex++
				var sample ThroughputSample
				var err error
				if direction == "download" {
					sample, err = c.measureDownloadTracked(ctx, profileName, plan.ChunkBytes, runIndex, activity)
				} else {
					sample, err = c.measureUploadTracked(ctx, profileName, plan.ChunkBytes, runIndex, activity)
				}
				if err != nil {
					aggregate.failure(err)
					select {
					case <-ctx.Done():
						return
					case <-stop:
						return
					case <-time.After(10 * time.Millisecond):
					}
					continue
				}
				aggregate.success(sample)
			}
		}(workerIndex)
	}

	windowStart := time.Now()
	close(startGate)

	timer := time.NewTimer(plan.WindowDuration)
	defer timer.Stop()
	probeContext, cancelProbes := context.WithCancel(ctx)
	defer cancelProbes()
	var stopOnce sync.Once
	stopWorkers := func() {
		stopOnce.Do(func() { close(stop) })
	}

	timerDone := false
	probeDone := !withLoadedLatency
	var probes []LatencySample
	var probeErr error
	probeResult := make(chan struct {
		samples []LatencySample
		err     error
	}, 1)
	if withLoadedLatency {
		go func() {
			samples, err := c.runLoadedLatencyProbes(probeContext, direction, plan.LoadedProbeCount, activity)
			probeResult <- struct {
				samples []LatencySample
				err     error
			}{samples: samples, err: err}
		}()
	}

	for !timerDone || !probeDone {
		select {
		case <-ctx.Done():
			stopWorkers()
			cancelProbes()
			workers.Wait()
			return ThroughputSample{}, probes, ctx.Err()
		case <-timer.C:
			timerDone = true
			// The configured duration is a stop deadline, not permission for
			// latency retries to extend the load window indefinitely. Existing
			// requests drain and remain in the aggregate; no new request begins.
			stopWorkers()
			cancelProbes()
		case result := <-probeResult:
			probes = result.samples
			probeErr = result.err
			probeDone = true
		}
	}

	stopWorkers()
	workers.Wait()
	windowEnd := time.Now()
	bytesTransferred, requestCount, lastErr := aggregate.snapshot()
	if bytesTransferred <= 0 || requestCount == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("no verified requests completed")
		}
		return ThroughputSample{}, probes, fmt.Errorf("%s window %d failed: %w", direction, windowIndex+1, lastErr)
	}
	if probeErr != nil {
		return ThroughputSample{}, probes, fmt.Errorf("%s loaded-latency window %d: %w", direction, windowIndex+1, probeErr)
	}

	duration := windowEnd.Sub(windowStart)
	if duration <= 0 {
		return ThroughputSample{}, probes, fmt.Errorf("%s window %d has invalid duration %s", direction, windowIndex+1, duration)
	}
	return ThroughputSample{
		Timestamp:    windowEnd,
		Direction:    direction,
		SizeBytes:    bytesTransferred,
		Duration:     duration,
		Mbps:         float64(bytesTransferred*8) / duration.Seconds() / 1e6,
		Profile:      "window",
		RunIndex:     windowIndex,
		SampleKind:   "window",
		WindowIndex:  windowIndex,
		Concurrency:  plan.Concurrency,
		ChunkBytes:   plan.ChunkBytes,
		RequestCount: requestCount,
		TimingSource: "aggregate-wall-clock",
	}, probes, nil
}

func (c *Client) runLoadedLatencyProbes(ctx context.Context, condition string, count int, activity *loadActivity) ([]LatencySample, error) {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	if count <= 0 {
		return nil, nil
	}
	samples := make([]LatencySample, 0, count)
	maxAttempts := count * 5
	var lastErr error

	for attempt := 0; attempt < maxAttempts && len(samples) < count; attempt++ {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		if err := activity.waitActive(ctx, 2*time.Second); err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		before := activity.snapshot()
		if before.active <= 0 {
			continue
		}
		startedAt := time.Now()
		probe, err := c.measureLatency(ctx, condition, attempt)
		endedAt := time.Now()
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		after := activity.snapshot()
		overlapped := before.active > 0 && after.active > 0 && before.gapGeneration == after.gapGeneration
		if !overlapped {
			lastErr = fmt.Errorf("probe did not remain inside a continuous load interval")
			continue
		}

		sample := LatencySample{
			Timestamp:            endedAt,
			StartedAt:            startedAt,
			EndedAt:              endedAt,
			RTT:                  probe.RTT,
			Condition:            condition,
			LoadOverlapped:       true,
			LoadTrackingAccurate: true,
			TimingSource:         probe.TimingSource,
			ConnectionReused:     probe.ConnectionReused,
			ProbeTransport:       probe.Transport,
			ProbeMethod:          probe.Method,
			ProbePath:            probe.Path,
			ProbeFallbackReason:  probe.FallbackReason,
			WebSocketProtocol:    probe.WebSocketProtocol,
		}
		samples = append(samples, sample)
		if c.cfg.OnProgress != nil {
			c.cfg.OnProgress("loaded-latency", len(samples), count, float64(probe.RTT.Microseconds())/1000)
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}

	if len(samples) < requiredSuccessfulRuns(count) {
		if lastErr == nil {
			lastErr = fmt.Errorf("insufficient overlapping probes")
		}
		return samples, fmt.Errorf("%s loaded latency produced %d/%d continuously-overlapped probes: %w",
			condition, len(samples), count, lastErr)
	}
	return samples, nil
}

// measureDownload measures one verified bounded request.
func (c *Client) measureDownload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	_netspeedProgress := nsBeginProgress("download measurement")
	defer _netspeedProgress.Done("complete")
	return c.measureDownloadTracked(ctx, profileName, numBytes, run, nil)
}

func (c *Client) measureDownloadTracked(
	ctx context.Context,
	profileName string,
	numBytes int64,
	run int,
	activity *loadActivity,
) (ThroughputSample, error) {
	_netspeedProgress := nsBeginProgress("download measurement")
	defer _netspeedProgress.Done("complete")
	if numBytes < 0 || numBytes > c.maxTransferBytes {
		return ThroughputSample{}, fmt.Errorf("download size %d exceeds negotiated maximum %d", numBytes, c.maxTransferBytes)
	}

	measID := fmt.Sprintf("%d-download-%d", time.Now().UnixNano(), run)
	requestURL := c.downloadMeasurementURL(numBytes, url.Values{
		"measId":  {measID},
		"profile": {profileName},
		"run":     {fmt.Sprintf("%d", run)},
	})

	var timing timingInfo
	trace := createTrace(&timing)
	traceContext := httptrace.WithClientTrace(ctx, trace)
	req, err := http.NewRequestWithContext(traceContext, http.MethodGet, requestURL, nil)
	if err != nil {
		return ThroughputSample{}, err
	}
	c.setMeasurementRequestHeaders(req)

	requestStart := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return ThroughputSample{}, err
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "application/octet-stream") {
		return ThroughputSample{}, fmt.Errorf("unexpected download content type %q", contentType)
	}
	if err := c.verifyDownloadMeasurementResponse(resp, numBytes, "download"); err != nil {
		return ThroughputSample{}, err
	}
	if resp.ContentLength >= 0 && resp.ContentLength != numBytes {
		return ThroughputSample{}, fmt.Errorf("download Content-Length %d; expected %d", resp.ContentLength, numBytes)
	}

	if activity != nil {
		activity.begin()
		defer activity.end()
	}
	received, err := consumeExactBody(resp.Body, numBytes)
	if err != nil {
		return ThroughputSample{}, fmt.Errorf("read download body: %w", err)
	}
	bodyDone := time.Now()
	if received != numBytes {
		return ThroughputSample{}, fmt.Errorf("download received %d bytes; expected %d", received, numBytes)
	}

	duration := bodyDone.Sub(requestStart)
	timingSource := "manual-body"
	if !timing.gotFirstByte.IsZero() {
		duration = bodyDone.Sub(timing.gotFirstByte)
		timingSource = "httptrace-body"
	}
	if duration <= 0 {
		return ThroughputSample{}, fmt.Errorf("invalid download duration %s", duration)
	}

	return ThroughputSample{
		Timestamp: bodyDone, Direction: "download", SizeBytes: received,
		Duration: duration, Mbps: float64(received*8) / duration.Seconds() / 1e6,
		Profile: profileName, RunIndex: run, TimingSource: timingSource,
	}, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type timedRequestBody struct {
	mu              sync.Mutex
	reader          io.Reader
	firstRead       time.Time
	lastRead        time.Time
	bytesRead       int64
	activity        *loadActivity
	activityStarted bool
}

func newTimedRequestBody(size int64) *timedRequestBody {
	return newTimedRequestBodyWithActivity(size, nil)
}

func newTimedRequestBodyWithActivity(size int64, activity *loadActivity) *timedRequestBody {
	return &timedRequestBody{
		reader:   &io.LimitedReader{R: zeroReader{}, N: size},
		activity: activity,
	}
}

func (body *timedRequestBody) Read(p []byte) (int, error) {
	started := time.Now()
	n, err := body.reader.Read(p)
	if n > 0 {
		body.mu.Lock()
		if body.firstRead.IsZero() {
			body.firstRead = started
			if body.activity != nil {
				body.activity.begin()
				body.activityStarted = true
			}
		}
		body.lastRead = time.Now()
		body.bytesRead += int64(n)
		body.mu.Unlock()
	}
	return n, err
}

func (body *timedRequestBody) Close() error { return nil }

func (body *timedRequestBody) finishActivity() {
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.activityStarted {
		body.activity.end()
		body.activityStarted = false
	}
}

func (body *timedRequestBody) duration() time.Duration {
	body.mu.Lock()
	defer body.mu.Unlock()
	if body.firstRead.IsZero() || body.lastRead.IsZero() {
		return 0
	}
	return body.lastRead.Sub(body.firstRead)
}

func requireMeasurementStatus(resp *http.Response, expected int) error {
	if resp.StatusCode == expected {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxMeasurementErrorBytes))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("measurement request returned HTTP %d: %s", resp.StatusCode, detail)
}

func buildMeasurementURL(baseURL, path string, values url.Values) string {
	return strings.TrimRight(baseURL, "/") + path + "?" + values.Encode()
}

func consumeExactBody(reader io.Reader, expected int64) (int64, error) {
	if expected < 0 {
		return 0, fmt.Errorf("invalid expected body length %d", expected)
	}
	limit := expected
	if expected < math.MaxInt64 {
		limit++
	}
	n, err := io.Copy(io.Discard, io.LimitReader(reader, limit))
	if err != nil {
		return n, err
	}
	if n != expected {
		return n, fmt.Errorf("received %d bytes; expected %d", n, expected)
	}
	return n, nil
}

func decodeLimitedJSON(reader io.Reader, maxBytes int64, destination any) error {
	if maxBytes <= 0 {
		return fmt.Errorf("invalid JSON body limit %d", maxBytes)
	}
	limit := maxBytes
	if maxBytes < math.MaxInt64 {
		limit++
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("JSON body exceeds %d bytes", maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("unexpected trailing JSON data: %w", err)
	}
	return nil
}

// measureUpload streams one bounded request and requires a receipt proving the
// server consumed the exact body.
func (c *Client) measureUpload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	_netspeedProgress := nsBeginProgress("upload measurement")
	defer _netspeedProgress.Done("complete")
	return c.measureUploadTracked(ctx, profileName, numBytes, run, nil)
}

func (c *Client) measureUploadTracked(
	ctx context.Context,
	profileName string,
	numBytes int64,
	run int,
	activity *loadActivity,
) (ThroughputSample, error) {
	_netspeedProgress := nsBeginProgress("upload measurement")
	defer _netspeedProgress.Done("complete")
	if numBytes < 0 || numBytes > c.maxTransferBytes {
		return ThroughputSample{}, fmt.Errorf("upload size %d exceeds negotiated maximum %d", numBytes, c.maxTransferBytes)
	}
	if c.uploadReceiptVersion < protocol.UploadReceiptVersion {
		return ThroughputSample{}, fmt.Errorf("server does not support verified upload receipts")
	}

	measID := fmt.Sprintf("%d-upload-%d", time.Now().UnixNano(), run)
	requestURL := c.uploadMeasurementURL(numBytes, url.Values{
		"measId":  {measID},
		"profile": {profileName},
		"run":     {fmt.Sprintf("%d", run)},
	})
	body := newTimedRequestBodyWithActivity(numBytes, activity)
	defer body.finishActivity()
	timing := timingInfo{}
	traceContext := httptrace.WithClientTrace(ctx, createTrace(&timing))

	req, err := http.NewRequestWithContext(traceContext, http.MethodPost, requestURL, body)
	if err != nil {
		return ThroughputSample{}, err
	}
	c.setMeasurementRequestHeaders(req)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", c.measurementTransport.UploadEncoding)
	req.ContentLength = numBytes

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return ThroughputSample{}, err
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return ThroughputSample{}, fmt.Errorf("unexpected upload receipt content type %q", contentType)
	}
	if err := c.verifyUploadMeasurementResponse(resp, numBytes); err != nil {
		return ThroughputSample{}, err
	}

	var receipt protocol.UploadReceipt
	if err := decodeLimitedJSON(resp.Body, maxUploadReceiptBodyBytes, &receipt); err != nil {
		return ThroughputSample{}, fmt.Errorf("decode upload receipt: %w", err)
	}
	if !receipt.OK {
		return ThroughputSample{}, fmt.Errorf("server rejected upload")
	}
	body.mu.Lock()
	bytesRead := body.bytesRead
	body.mu.Unlock()
	if bytesRead != numBytes {
		return ThroughputSample{}, fmt.Errorf("HTTP transport consumed %d upload bytes; expected %d", bytesRead, numBytes)
	}
	if receipt.AcceptedBytes != numBytes {
		return ThroughputSample{}, fmt.Errorf("server accepted %d upload bytes; expected %d", receipt.AcceptedBytes, numBytes)
	}

	duration := time.Duration(receipt.ServerDurationNS)
	timingSource := "server-receipt"
	if duration <= 0 {
		duration = body.duration()
		timingSource = "client-body"
	}
	if duration <= 0 {
		return ThroughputSample{}, fmt.Errorf("invalid upload duration %s", duration)
	}

	return ThroughputSample{
		Timestamp: time.Now(), Direction: "upload", SizeBytes: receipt.AcceptedBytes,
		Duration: duration, Mbps: float64(receipt.AcceptedBytes*8) / duration.Seconds() / 1e6,
		Profile: profileName, RunIndex: run, TimingSource: timingSource,
	}, nil
}

// runPacketLossTest runs the WebRTC packet loss test.
func (c *Client) runPacketLossTest(ctx context.Context) (*PacketLossResult, error) {
	_netspeedProgress := nsBeginProgress("packet delivery test")
	defer _netspeedProgress.Done("complete")
	// Use WebRTC implementation with pion/webrtc
	return c.runPacketLossTestWebRTC(ctx)
}

// throughputValues returns fixed-window samples when available. Baseline
// requests exist only to tune the window plan and must not bias the headline
// speed on fast links.
func throughputValues(samples []ThroughputSample, direction string) []float64 {
	windowValues := make([]float64, 0, len(samples))
	fallbackValues := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.Direction != direction || sample.Duration < 10*time.Millisecond {
			continue
		}
		fallbackValues = append(fallbackValues, sample.Mbps)
		if sample.SampleKind == "window" || sample.Profile == "window" {
			windowValues = append(windowValues, sample.Mbps)
		}
	}
	if len(windowValues) > 0 {
		return measurement.FilterIQR(windowValues)
	}
	return measurement.FilterIQR(fallbackValues)
}

func latencyValues(samples []LatencySample, condition string, requireOverlap bool) []float64 {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.Condition != condition || (requireOverlap && !sample.LoadOverlapped) {
			continue
		}
		values = append(values, float64(sample.RTT.Microseconds())/1000)
	}
	return values
}

// calculateSummary computes the shared R-7/IQR summary statistics.
func (c *Client) calculateSummary(results *Results) Summary {
	download := throughputValues(results.ThroughputSamples, "download")
	upload := throughputValues(results.ThroughputSamples, "upload")
	unloaded := measurement.PrepareLatency(latencyValues(results.LatencySamples, "unloaded", false), 2)
	downloadLoaded := measurement.FilterIQR(latencyValues(results.LatencySamples, "download", true))
	uploadLoaded := measurement.FilterIQR(latencyValues(results.LatencySamples, "upload", true))

	summary := Summary{
		DownloadMbps:      measurement.Percentile(download, 90),
		UploadMbps:        measurement.Percentile(upload, 90),
		LatencyUnloadedMs: measurement.Percentile(unloaded, 50),
		LatencyDownloadMs: measurement.Percentile(downloadLoaded, 90),
		LatencyUploadMs:   measurement.Percentile(uploadLoaded, 90),
		JitterMs:          measurement.Jitter(unloaded),
	}
	if results.PacketLoss != nil && !results.PacketLoss.Unavailable {
		loss := results.PacketLoss.TransactionLossPercent
		if loss == 0 && results.PacketLoss.LossPercent != 0 {
			loss = results.PacketLoss.LossPercent
		}
		summary.PacketLossPercent = &loss
	}
	return summary
}

func hasImpreciseTiming(results *Results) bool {
	for _, sample := range results.ThroughputSamples {
		if sample.SampleKind != "window" {
			continue
		}
		if sample.TimingSource != "aggregate-wall-clock" {
			return true
		}
	}
	for _, sample := range results.LatencySamples {
		if sample.TimingSource != "" && sample.TimingSource != "httptrace" {
			return true
		}
		if sample.LoadOverlapped && !sample.LoadTrackingAccurate {
			return true
		}
	}
	return false
}

func countWindows(samples []ThroughputSample, direction string) int {
	_netspeedProgress := nsBeginProgress("sustained measurement window")
	defer _netspeedProgress.Done("complete")
	count := 0
	for _, sample := range samples {
		if sample.Direction == direction && (sample.SampleKind == "window" || sample.Profile == "window") {
			count++
		}
	}
	return count
}

func countLatency(samples []LatencySample, condition string, overlapOnly bool) int {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	count := 0
	for _, sample := range samples {
		if sample.Condition == condition && (!overlapOnly || sample.LoadOverlapped) {
			count++
		}
	}
	return count
}

// assessTestConfidence is mirrored in web/js/speedtest.js. The score uses five
// independently visible gates instead of allowing an unavailable measurement
// to look like a stable zero.
func (c *Client) assessTestConfidence(results *Results) TestConfidence {
	var confidence TestConfidence
	downloadExpected := !c.cfg.UploadOnly
	uploadExpected := !c.cfg.DownloadOnly

	downloadValues := throughputValues(results.ThroughputSamples, "download")
	uploadValues := throughputValues(results.ThroughputSamples, "upload")
	unloadedValues := measurement.PrepareLatency(latencyValues(results.LatencySamples, "unloaded", false), 2)

	downloadWindows := countWindows(results.ThroughputSamples, "download")
	uploadWindows := countWindows(results.ThroughputSamples, "upload")
	unloadedCount := countLatency(results.LatencySamples, "unloaded", false)
	downloadLoaded := countLatency(results.LatencySamples, "download", true)
	uploadLoaded := countLatency(results.LatencySamples, "upload", true)

	sampleAdequate := unloadedCount >= 10
	if downloadExpected {
		sampleAdequate = sampleAdequate && downloadWindows >= 3 && downloadLoaded >= 3
	}
	if uploadExpected {
		sampleAdequate = sampleAdequate && uploadWindows >= 3 && uploadLoaded >= 3
	}
	confidence.Metrics.SampleCount = ConfidenceSampleCount{
		DownloadWindows:       downloadWindows,
		UploadWindows:         uploadWindows,
		UnloadedLatency:       unloadedCount,
		DownloadLoadedLatency: downloadLoaded,
		UploadLoadedLatency:   uploadLoaded,
		Adequate:              sampleAdequate,
	}

	downloadCV := measurement.CoefficientOfVariation(downloadValues)
	uploadCV := measurement.CoefficientOfVariation(uploadValues)
	latencyCV := measurement.CoefficientOfVariation(unloadedValues)
	variabilityAcceptable := latencyCV < 50
	if downloadExpected {
		variabilityAcceptable = variabilityAcceptable && downloadCV < 30
	}
	if uploadExpected {
		variabilityAcceptable = variabilityAcceptable && uploadCV < 30
	}
	confidence.Metrics.Variability = ConfidenceVariability{
		Download:   downloadCV,
		Upload:     uploadCV,
		Latency:    latencyCV,
		Acceptable: variabilityAcceptable,
	}

	overlapComplete := true
	if downloadExpected {
		overlapComplete = overlapComplete && downloadLoaded >= 3
	}
	if uploadExpected {
		overlapComplete = overlapComplete && uploadLoaded >= 3
	}
	confidence.Metrics.LoadedOverlap = ConfidenceOverlap{
		DownloadAccepted: downloadLoaded,
		UploadAccepted:   uploadLoaded,
		Complete:         overlapComplete,
	}

	packetComplete := results.PacketLoss != nil &&
		!results.PacketLoss.Unavailable &&
		results.PacketLoss.ForwardLossPercent != nil &&
		results.PacketLoss.AcknowledgementsSent > 0 &&
		results.PacketLoss.ReverseAcknowledgementLossPercent != nil
	confidence.Metrics.PacketTest = ConfidencePacketTest{Completed: packetComplete}
	timingAccurate := !hasImpreciseTiming(results)
	confidence.Metrics.Timing = ConfidenceTiming{Accurate: timingAccurate}

	score := 100
	if !sampleAdequate {
		score -= 20
		confidence.Warnings = append(confidence.Warnings, "Insufficient fixed-window or latency samples for high confidence")
	}
	if !variabilityAcceptable {
		score -= 25
		confidence.Warnings = append(confidence.Warnings, "High variability in measurements")
	}
	if !overlapComplete {
		score -= 25
		confidence.Warnings = append(confidence.Warnings, "Loaded-latency overlap was incomplete")
	}
	if !packetComplete {
		score -= 20
		confidence.Warnings = append(confidence.Warnings, "Directional packet-loss test incomplete")
	}
	if !timingAccurate {
		score -= 10
		confidence.Warnings = append(confidence.Warnings, "Some measurements used fallback timing")
	}
	if score < 0 {
		score = 0
	}
	confidence.OverallScore = score
	switch {
	case score >= 80:
		confidence.Overall = "high"
	case score >= 50:
		confidence.Overall = "medium"
	default:
		confidence.Overall = "low"
	}
	return confidence
}

// calculateQuality determines network quality grades.
func (c *Client) calculateQuality(s Summary) NetworkQuality {
	return NetworkQuality{
		VideoStreaming: gradeForStreaming(s),
		Gaming:         gradeForGaming(s),
		VideoChatting:  gradeForVideoChat(s),
	}
}

func gradeForStreaming(s Summary) string {
	if s.PacketLossPercent == nil {
		return "Incomplete"
	}
	loss := *s.PacketLossPercent
	if s.DownloadMbps >= 50 && s.LatencyUnloadedMs <= 25 &&
		s.JitterMs <= 5 && loss <= 0.5 {
		return "Great"
	}
	if s.DownloadMbps >= 20 && s.LatencyUnloadedMs <= 50 &&
		s.JitterMs <= 15 && loss <= 1.5 {
		return "Good"
	}
	if s.DownloadMbps >= 10 && s.LatencyUnloadedMs <= 80 &&
		s.JitterMs <= 30 && loss <= 3 {
		return "Okay"
	}
	return "Poor"
}

func gradeForGaming(s Summary) string {
	if s.PacketLossPercent == nil {
		return "Incomplete"
	}
	loss := *s.PacketLossPercent
	if s.DownloadMbps >= 25 && s.LatencyUnloadedMs <= 20 &&
		s.JitterMs <= 5 && loss <= 0.1 {
		return "Great"
	}
	if s.DownloadMbps >= 15 && s.LatencyUnloadedMs <= 40 &&
		s.JitterMs <= 10 && loss <= 0.5 {
		return "Good"
	}
	if s.DownloadMbps >= 5 && s.LatencyUnloadedMs <= 80 &&
		s.JitterMs <= 20 && loss <= 1 {
		return "Okay"
	}
	return "Poor"
}

func gradeForVideoChat(s Summary) string {
	if s.PacketLossPercent == nil {
		return "Incomplete"
	}
	loss := *s.PacketLossPercent
	if s.DownloadMbps >= 10 && s.UploadMbps >= 5 &&
		s.LatencyUnloadedMs <= 50 &&
		s.JitterMs <= 10 && loss <= 1 {
		return "Great"
	}
	if s.DownloadMbps >= 5 && s.UploadMbps >= 2 &&
		s.LatencyUnloadedMs <= 100 &&
		s.JitterMs <= 20 && loss <= 2 {
		return "Good"
	}
	if s.DownloadMbps >= 2 && s.UploadMbps >= 1 &&
		s.LatencyUnloadedMs <= 150 &&
		s.JitterMs <= 40 && loss <= 5 {
		return "Okay"
	}
	return "Poor"
}
