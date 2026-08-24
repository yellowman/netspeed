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

	"github.com/yellowman/netspeed/internal/protocol"
)

// timingInfo captures precise HTTP timing events.
type timingInfo struct {
	wroteRequest time.Time
	gotFirstByte time.Time
}

// createTrace creates an httptrace.ClientTrace for precise timing.
func createTrace(t *timingInfo) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
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
	ServerURL      string
	Timeout        time.Duration
	Quick          bool
	DownloadOnly   bool
	UploadOnly     bool
	SkipPacketLoss bool
	OnProgress     func(phase string, current, total int, value float64)
}

// Buffer sizes for high-speed connections
const (
	ReadBufferSize            = 4 * 1024 * 1024 // 4MB read buffer
	WriteBufferSize           = 4 * 1024 * 1024 // 4MB write buffer
	legacyMaxTransferBytes    = 100_000_000     // conservative cap for older /meta responses
	maxMeasurementErrorBytes  = 4 * 1024
	maxMetaBodyBytes          = 1 * 1024 * 1024
	maxUploadReceiptBodyBytes = 64 * 1024
)

// UserAgent matches python-requests default format
const UserAgent = "python-requests/2.32.0"

// setRequestHeaders adds headers matching python requests library defaults
func setRequestHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")
}

// Time budget constants (matching web client)
const (
	MaxTestDuration         = 4 * time.Second // Max time for single profile to be selected
	TotalPhaseBudget        = 8 * time.Second // Total time budget per phase
	LowLatencyThreshold     = 50 * time.Millisecond
	HighLatencyThreshold    = 100 * time.Millisecond
	MinBandwidthForParallel = 2.0 // Mbps
)

// Client performs speed tests.
type Client struct {
	cfg                  Config
	httpClient           *http.Client
	maxTransferBytes     int64
	uploadReceiptVersion int
}

// New creates a new speed test client.
func New(cfg Config) *Client {
	// Custom dialer with TCP optimizations
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return &Client{
		cfg:              cfg,
		maxTransferBytes: legacyMaxTransferBytes,
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
	if meta.MaxTransferBytes > 0 {
		c.maxTransferBytes = meta.MaxTransferBytes
	}
	c.uploadReceiptVersion = meta.UploadReceiptVersion
	if !c.cfg.DownloadOnly && c.uploadReceiptVersion < protocol.UploadReceiptVersion {
		return nil, fmt.Errorf("server does not support verified upload receipts (need version %d)", protocol.UploadReceiptVersion)
	}

	// Run latency tests (unloaded) with adaptive batching
	latencySamples, err := c.runAdaptiveLatencyTest(ctx, "unloaded", 20)
	if err != nil {
		return nil, fmt.Errorf("latency test failed: %w", err)
	}
	results.LatencySamples = append(results.LatencySamples, latencySamples...)

	// Run download tests with adaptive profile selection
	if !c.cfg.UploadOnly {
		downloadSamples, loadedLatency, err := c.runDownloadTests(ctx)
		if err != nil {
			return nil, fmt.Errorf("download test failed: %w", err)
		}
		results.ThroughputSamples = append(results.ThroughputSamples, downloadSamples...)
		results.LatencySamples = append(results.LatencySamples, loadedLatency...)
	}

	// Run upload tests with adaptive profile selection
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

	return results, nil
}

// fetchMeta fetches server and client metadata.
func (c *Client) fetchMeta(ctx context.Context) (*Meta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.ServerURL+"/meta", nil)
	if err != nil {
		return nil, err
	}
	setRequestHeaders(req)

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
func (c *Client) runAdaptiveLatencyTest(ctx context.Context, phase string, count int) ([]LatencySample, error) {
	if c.cfg.Quick {
		count = 5
	}

	samples := make([]LatencySample, 0, count)

	// Phase 1: Run first 3 probes sequentially to estimate connection quality
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

		rtt, err := c.measureLatency(ctx, phase, i)
		if err != nil {
			continue
		}

		sample := LatencySample{
			Timestamp: time.Now(),
			RTT:       rtt,
			Phase:     phase,
		}
		samples = append(samples, sample)

		if c.cfg.OnProgress != nil {
			c.cfg.OnProgress("latency", i+1, count, float64(rtt.Milliseconds()))
		}
	}

	if len(samples) == 0 {
		return samples, fmt.Errorf("%s latency test produced no valid samples", phase)
	}

	// Phase 2: Decide batching strategy based on median RTT
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

	// Phase 3: Run remaining probes
	remaining := count - len(samples)
	if useParallel {
		// Batch 5 probes at a time
		batchSize := 5
		for i := 0; i < remaining; i += batchSize {
			batch := batchSize
			if i+batch > remaining {
				batch = remaining - i
			}

			batchSamples := c.runParallelLatencyProbes(ctx, phase, len(samples), batch)
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

			rtt, err := c.measureLatency(ctx, phase, i)
			if err != nil {
				continue
			}

			sample := LatencySample{
				Timestamp: time.Now(),
				RTT:       rtt,
				Phase:     phase,
			}
			samples = append(samples, sample)

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("latency", i+1, count, float64(rtt.Milliseconds()))
			}
		}
	}

	if len(samples) < requiredSuccessfulRuns(count) {
		return samples, fmt.Errorf("%s latency test produced %d/%d valid samples", phase, len(samples), count)
	}
	return samples, nil
}

// runParallelLatencyProbes runs multiple latency probes in parallel.
func (c *Client) runParallelLatencyProbes(ctx context.Context, phase string, startSeq, count int) []LatencySample {
	var wg sync.WaitGroup
	results := make(chan LatencySample, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			rtt, err := c.measureLatency(ctx, phase, seq)
			if err != nil {
				return
			}
			results <- LatencySample{
				Timestamp: time.Now(),
				RTT:       rtt,
				Phase:     phase,
			}
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

// measureLatency measures a single latency probe using precise timing.
// RTT = GotFirstResponseByte - WroteRequest (excludes connection setup, TLS, DNS)
func (c *Client) measureLatency(ctx context.Context, phase string, seq int) (time.Duration, error) {
	measID := fmt.Sprintf("%d-%s-%d", time.Now().UnixNano(), phase, seq)
	requestURL := buildMeasurementURL(c.cfg.ServerURL, "/__down", url.Values{
		"bytes":  {"0"},
		"measId": {measID},
		"during": {phase},
		"seq":    {fmt.Sprintf("%d", seq)},
	})

	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, err
	}
	setRequestHeaders(req)
	req.Header.Set("Cache-Control", "no-store")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return 0, err
	}
	received, err := consumeExactBody(resp.Body, 0)
	if err != nil {
		return 0, fmt.Errorf("read latency response: %w", err)
	}
	if received != 0 {
		return 0, fmt.Errorf("latency response contained %d bytes; expected 0", received)
	}

	if timing.wroteRequest.IsZero() || timing.gotFirstByte.IsZero() {
		return 0, fmt.Errorf("timing trace failed")
	}
	rtt := timing.gotFirstByte.Sub(timing.wroteRequest)
	if rtt <= 0 {
		return 0, fmt.Errorf("invalid latency duration %s", rtt)
	}
	return rtt, nil
}

// Profile configuration
type profile struct {
	Name  string
	Bytes int64
	Runs  int
}

// All download profiles matching web client (up to 1 Tbps)
var allDownloadProfiles = []profile{
	{"100kB", 100_000, 10},
	{"1MB", 1_000_000, 8},
	{"10MB", 10_000_000, 6},
	{"25MB", 25_000_000, 4},
	{"100MB", 100_000_000, 3},
	{"250MB", 250_000_000, 2},
	{"500MB", 500_000_000, 2},     // 1s at 4 Gbps
	{"1GB", 1_000_000_000, 2},     // 1s at 8 Gbps
	{"2GB", 2_000_000_000, 2},     // 1s at 16 Gbps
	{"5GB", 5_000_000_000, 2},     // 1s at 40 Gbps
	{"12GB", 12_000_000_000, 2},   // 1s at ~100 Gbps
	{"50GB", 50_000_000_000, 2},   // 1s at 400 Gbps
	{"100GB", 100_000_000_000, 2}, // 1s at 800 Gbps
	{"125GB", 125_000_000_000, 2}, // 1s at 1 Tbps
}

// All upload profiles matching web client (up to 1 Tbps)
var allUploadProfiles = []profile{
	{"100kB", 100_000, 8},
	{"1MB", 1_000_000, 6},
	{"10MB", 10_000_000, 4},
	{"25MB", 25_000_000, 4},
	{"50MB", 50_000_000, 3},
	{"100MB", 100_000_000, 2},
	{"250MB", 250_000_000, 2},     // 1s at 2 Gbps
	{"500MB", 500_000_000, 2},     // 1s at 4 Gbps
	{"1GB", 1_000_000_000, 2},     // 1s at 8 Gbps
	{"2GB", 2_000_000_000, 2},     // 1s at 16 Gbps
	{"5GB", 5_000_000_000, 2},     // 1s at 40 Gbps
	{"12GB", 12_000_000_000, 2},   // 1s at ~100 Gbps
	{"50GB", 50_000_000_000, 2},   // 1s at 400 Gbps
	{"100GB", 100_000_000_000, 2}, // 1s at 800 Gbps
	{"125GB", 125_000_000_000, 2}, // 1s at 1 Tbps
}

var quickDownloadProfiles = []profile{
	{"100kB", 100_000, 3},
	{"1MB", 1_000_000, 3},
}

var quickUploadProfiles = []profile{
	{"100kB", 100_000, 3},
	{"1MB", 1_000_000, 3},
}

// Baseline profiles (always run first)
var baselineDownloadProfiles = []profile{
	{"100kB", 100_000, 10},
	{"1MB", 1_000_000, 8},
}

var baselineUploadProfiles = []profile{
	{"100kB", 100_000, 8},
	{"1MB", 1_000_000, 6},
}

// estimateTransferTime estimates how long a transfer will take.
func estimateTransferTime(bytes int64, speedMbps float64) time.Duration {
	if speedMbps <= 0 {
		return time.Hour // effectively infinite
	}
	seconds := float64(bytes*8) / (speedMbps * 1e6)
	return time.Duration(seconds * float64(time.Second))
}

// selectProfiles selects profiles based on estimated speed.
func selectProfiles(estimatedSpeed float64, allProfiles []profile, baseline []profile, maxBytes int64) []profile {
	// Always include baseline
	selected := make([]profile, len(baseline))
	copy(selected, baseline)

	// Add larger profiles based on estimated transfer time
	for _, p := range allProfiles {
		// Skip baseline profiles (already included)
		isBaseline := false
		for _, b := range baseline {
			if p.Name == b.Name {
				isBaseline = true
				break
			}
		}
		if isBaseline {
			continue
		}

		if p.Bytes <= maxBytes && estimateTransferTime(p.Bytes, estimatedSpeed) <= MaxTestDuration {
			selected = append(selected, p)
		}
	}

	return selected
}

func profilesWithinLimit(profiles []profile, maxBytes int64) []profile {
	filtered := make([]profile, 0, len(profiles))
	for _, p := range profiles {
		if p.Bytes <= maxBytes {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func requiredSuccessfulRuns(total int) int {
	if total <= 1 {
		return total
	}
	return (total + 1) / 2
}

// runDownloadTests runs download speed tests with adaptive profile selection.
func (c *Client) runDownloadTests(ctx context.Context) ([]ThroughputSample, []LatencySample, error) {
	if c.cfg.Quick {
		profiles := profilesWithinLimit(quickDownloadProfiles, c.maxTransferBytes)
		if len(profiles) == 0 {
			return nil, nil, fmt.Errorf("server transfer limit %d is below the smallest download profile", c.maxTransferBytes)
		}
		samples, err := c.runProfiles(ctx, profiles, "download")
		return samples, nil, err
	}

	baselineProfiles := profilesWithinLimit(baselineDownloadProfiles, c.maxTransferBytes)
	if len(baselineProfiles) == 0 {
		return nil, nil, fmt.Errorf("server transfer limit %d is below the smallest download profile", c.maxTransferBytes)
	}

	var samples []ThroughputSample
	var loadedLatency []LatencySample
	phaseStart := time.Now()

	// Phase 1: Run baseline profiles
	baselineSamples, err := c.runProfiles(ctx, baselineProfiles, "download")
	if err != nil {
		return nil, nil, err
	}
	samples = append(samples, baselineSamples...)

	// Phase 2: Estimate speed from 1MB samples
	var mbSpeeds []float64
	for _, s := range samples {
		if s.Profile == "1MB" {
			mbSpeeds = append(mbSpeeds, s.Mbps)
		}
	}

	estimatedSpeed := 10.0 // default
	if len(mbSpeeds) > 0 {
		sort.Float64s(mbSpeeds)
		estimatedSpeed = mbSpeeds[len(mbSpeeds)/2] // median
	}

	// Phase 3: Select and run larger profiles within time budget
	selectedProfiles := selectProfiles(estimatedSpeed, allDownloadProfiles, baselineProfiles, c.maxTransferBytes)

	for _, p := range selectedProfiles {
		// Skip baseline (already run)
		isBaseline := false
		for _, b := range baselineProfiles {
			if p.Name == b.Name {
				isBaseline = true
				break
			}
		}
		if isBaseline {
			continue
		}

		// Check time budget
		elapsed := time.Since(phaseStart)
		remaining := TotalPhaseBudget - elapsed
		estimatedBatchTime := estimateTransferTime(p.Bytes, estimatedSpeed) * time.Duration(p.Runs)

		if estimatedBatchTime > remaining {
			continue // Skip this batch
		}

		// Run profile
		profileSamples, err := c.runProfiles(ctx, []profile{p}, "download")
		if err != nil {
			continue
		}
		samples = append(samples, profileSamples...)

		// Run loaded latency probes during large downloads
		if p.Bytes >= 10_000_000 && len(loadedLatency) < 5 {
			latSamples, _ := c.runLatencyProbes(ctx, "download", 5-len(loadedLatency))
			loadedLatency = append(loadedLatency, latSamples...)
		}
	}

	return samples, loadedLatency, nil
}

// runUploadTests runs upload speed tests with adaptive profile selection.
func (c *Client) runUploadTests(ctx context.Context) ([]ThroughputSample, []LatencySample, error) {
	if c.cfg.Quick {
		profiles := profilesWithinLimit(quickUploadProfiles, c.maxTransferBytes)
		if len(profiles) == 0 {
			return nil, nil, fmt.Errorf("server transfer limit %d is below the smallest upload profile", c.maxTransferBytes)
		}
		samples, err := c.runUploadProfiles(ctx, profiles)
		return samples, nil, err
	}

	baselineProfiles := profilesWithinLimit(baselineUploadProfiles, c.maxTransferBytes)
	if len(baselineProfiles) == 0 {
		return nil, nil, fmt.Errorf("server transfer limit %d is below the smallest upload profile", c.maxTransferBytes)
	}

	var samples []ThroughputSample
	var loadedLatency []LatencySample
	phaseStart := time.Now()

	// Phase 1: Run baseline profiles
	baselineSamples, err := c.runUploadProfiles(ctx, baselineProfiles)
	if err != nil {
		return nil, nil, err
	}
	samples = append(samples, baselineSamples...)

	// Phase 2: Estimate speed from 1MB samples
	var mbSpeeds []float64
	for _, s := range samples {
		if s.Profile == "1MB" {
			mbSpeeds = append(mbSpeeds, s.Mbps)
		}
	}

	estimatedSpeed := 10.0 // default
	if len(mbSpeeds) > 0 {
		sort.Float64s(mbSpeeds)
		estimatedSpeed = mbSpeeds[len(mbSpeeds)/2] // median
	}

	// Phase 3: Select and run larger profiles within time budget
	selectedProfiles := selectProfiles(estimatedSpeed, allUploadProfiles, baselineProfiles, c.maxTransferBytes)

	for _, p := range selectedProfiles {
		// Skip baseline (already run)
		isBaseline := false
		for _, b := range baselineProfiles {
			if p.Name == b.Name {
				isBaseline = true
				break
			}
		}
		if isBaseline {
			continue
		}

		// Check time budget
		elapsed := time.Since(phaseStart)
		remaining := TotalPhaseBudget - elapsed
		estimatedBatchTime := estimateTransferTime(p.Bytes, estimatedSpeed) * time.Duration(p.Runs)

		if estimatedBatchTime > remaining {
			continue // Skip this batch
		}

		// Run profile
		profileSamples, err := c.runUploadProfiles(ctx, []profile{p})
		if err != nil {
			continue
		}
		samples = append(samples, profileSamples...)

		// Run loaded latency probes during large uploads
		if p.Bytes >= 10_000_000 && len(loadedLatency) < 5 {
			latSamples, _ := c.runLatencyProbes(ctx, "upload", 5-len(loadedLatency))
			loadedLatency = append(loadedLatency, latSamples...)
		}
	}

	return samples, loadedLatency, nil
}

// runLatencyProbes runs a few latency probes.
func (c *Client) runLatencyProbes(ctx context.Context, phase string, count int) ([]LatencySample, error) {
	samples := make([]LatencySample, 0, count)
	var lastErr error
	for i := 0; i < count; i++ {
		rtt, err := c.measureLatency(ctx, phase, i)
		if err != nil {
			lastErr = err
			continue
		}
		samples = append(samples, LatencySample{
			Timestamp: time.Now(),
			RTT:       rtt,
			Phase:     phase,
		})
	}
	if len(samples) < requiredSuccessfulRuns(count) {
		if lastErr == nil {
			lastErr = fmt.Errorf("measurement canceled or incomplete")
		}
		return samples, fmt.Errorf("%s latency probes produced %d/%d valid samples: %w",
			phase, len(samples), count, lastErr)
	}
	return samples, nil
}

// runProfiles runs download profiles.
func (c *Client) runProfiles(ctx context.Context, profiles []profile, direction string) ([]ThroughputSample, error) {
	var samples []ThroughputSample
	totalRuns := 0
	for _, p := range profiles {
		totalRuns += p.Runs
	}

	currentRun := 0
	for _, p := range profiles {
		profileSamples := make([]ThroughputSample, 0, p.Runs)
		var lastErr error
		for run := 0; run < p.Runs; run++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureDownload(ctx, p.Name, p.Bytes, run)
			if err != nil {
				lastErr = err
				continue
			}

			profileSamples = append(profileSamples, sample)
			currentRun++
			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress(direction, currentRun, totalRuns, sample.Mbps)
			}
		}

		if len(profileSamples) < requiredSuccessfulRuns(p.Runs) {
			if lastErr == nil {
				lastErr = fmt.Errorf("measurement canceled or incomplete")
			}
			return samples, fmt.Errorf("%s profile %s produced %d/%d valid samples: %w",
				direction, p.Name, len(profileSamples), p.Runs, lastErr)
		}
		samples = append(samples, profileSamples...)
	}

	return samples, nil
}

// measureDownload measures a single download using precise timing.
// Body transfer time = bodyDone - GotFirstResponseByte (excludes connection, TLS, headers).
func (c *Client) measureDownload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	if numBytes < 0 || numBytes > c.maxTransferBytes {
		return ThroughputSample{}, fmt.Errorf("download size %d exceeds negotiated maximum %d", numBytes, c.maxTransferBytes)
	}

	measID := fmt.Sprintf("%d-download-%d", time.Now().UnixNano(), run)
	requestURL := buildMeasurementURL(c.cfg.ServerURL, "/__down", url.Values{
		"bytes":   {fmt.Sprintf("%d", numBytes)},
		"measId":  {measID},
		"profile": {profileName},
		"run":     {fmt.Sprintf("%d", run)},
	})

	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return ThroughputSample{}, err
	}
	setRequestHeaders(req)
	req.Header.Set("Cache-Control", "no-store")

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
	if resp.ContentLength >= 0 && resp.ContentLength != numBytes {
		return ThroughputSample{}, fmt.Errorf("download Content-Length %d; expected %d", resp.ContentLength, numBytes)
	}

	received, err := consumeExactBody(resp.Body, numBytes)
	if err != nil {
		return ThroughputSample{}, fmt.Errorf("read download body: %w", err)
	}
	bodyDone := time.Now()
	if received != numBytes {
		return ThroughputSample{}, fmt.Errorf("download received %d bytes; expected %d", received, numBytes)
	}

	var duration time.Duration
	if !timing.gotFirstByte.IsZero() {
		duration = bodyDone.Sub(timing.gotFirstByte)
	} else {
		duration = bodyDone.Sub(requestStart)
	}
	if duration <= 0 {
		return ThroughputSample{}, fmt.Errorf("invalid download duration %s", duration)
	}

	mbps := float64(received*8) / duration.Seconds() / 1e6
	return ThroughputSample{
		Timestamp: time.Now(), Direction: "download", SizeBytes: received,
		Duration: duration, Mbps: mbps, Profile: profileName, RunIndex: run,
	}, nil
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type timedRequestBody struct {
	reader    io.Reader
	firstRead time.Time
	lastRead  time.Time
	bytesRead int64
}

func newTimedRequestBody(size int64) *timedRequestBody {
	return &timedRequestBody{reader: &io.LimitedReader{R: zeroReader{}, N: size}}
}

func (b *timedRequestBody) Read(p []byte) (int, error) {
	started := time.Now()
	n, err := b.reader.Read(p)
	if n > 0 {
		if b.firstRead.IsZero() {
			b.firstRead = started
		}
		b.lastRead = time.Now()
		b.bytesRead += int64(n)
	}
	return n, err
}

func (b *timedRequestBody) Close() error { return nil }

func (b *timedRequestBody) duration() time.Duration {
	if b.firstRead.IsZero() || b.lastRead.IsZero() {
		return 0
	}
	return b.lastRead.Sub(b.firstRead)
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

func consumeExactBody(r io.Reader, expected int64) (int64, error) {
	if expected < 0 {
		return 0, fmt.Errorf("invalid expected body length %d", expected)
	}

	limit := expected
	if expected < math.MaxInt64 {
		limit++
	}
	n, err := io.Copy(io.Discard, io.LimitReader(r, limit))
	if err != nil {
		return n, err
	}
	if n != expected {
		return n, fmt.Errorf("received %d bytes; expected %d", n, expected)
	}
	return n, nil
}

func decodeLimitedJSON(r io.Reader, maxBytes int64, dst any) error {
	if maxBytes <= 0 {
		return fmt.Errorf("invalid JSON body limit %d", maxBytes)
	}

	limit := maxBytes
	if maxBytes < math.MaxInt64 {
		limit++
	}
	body, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxBytes {
		return fmt.Errorf("JSON body exceeds %d bytes", maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(dst); err != nil {
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

// runUploadProfiles runs upload profiles.
func (c *Client) runUploadProfiles(ctx context.Context, profiles []profile) ([]ThroughputSample, error) {
	var samples []ThroughputSample
	totalRuns := 0
	for _, p := range profiles {
		totalRuns += p.Runs
	}

	currentRun := 0
	for _, p := range profiles {
		profileSamples := make([]ThroughputSample, 0, p.Runs)
		var lastErr error
		for run := 0; run < p.Runs; run++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureUpload(ctx, p.Name, p.Bytes, run)
			if err != nil {
				lastErr = err
				continue
			}

			profileSamples = append(profileSamples, sample)
			currentRun++
			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("upload", currentRun, totalRuns, sample.Mbps)
			}
		}

		if len(profileSamples) < requiredSuccessfulRuns(p.Runs) {
			if lastErr == nil {
				lastErr = fmt.Errorf("measurement canceled or incomplete")
			}
			return samples, fmt.Errorf("upload profile %s produced %d/%d valid samples: %w",
				p.Name, len(profileSamples), p.Runs, lastErr)
		}
		samples = append(samples, profileSamples...)
	}

	return samples, nil
}

// measureUpload streams a single upload and requires a server receipt proving
// that the complete body was accepted.
func (c *Client) measureUpload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	if numBytes < 0 || numBytes > c.maxTransferBytes {
		return ThroughputSample{}, fmt.Errorf("upload size %d exceeds negotiated maximum %d", numBytes, c.maxTransferBytes)
	}
	if c.uploadReceiptVersion < protocol.UploadReceiptVersion {
		return ThroughputSample{}, fmt.Errorf("server does not support verified upload receipts")
	}

	measID := fmt.Sprintf("%d-upload-%d", time.Now().UnixNano(), run)
	requestURL := buildMeasurementURL(c.cfg.ServerURL, "/__up", url.Values{
		"measId":  {measID},
		"profile": {profileName},
		"run":     {fmt.Sprintf("%d", run)},
	})
	body := newTimedRequestBody(numBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, body)
	if err != nil {
		return ThroughputSample{}, err
	}
	setRequestHeaders(req)
	req.Header.Set("Content-Type", "application/octet-stream")
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

	var receipt protocol.UploadReceipt
	if err := decodeLimitedJSON(resp.Body, maxUploadReceiptBodyBytes, &receipt); err != nil {
		return ThroughputSample{}, fmt.Errorf("decode upload receipt: %w", err)
	}
	if !receipt.OK {
		return ThroughputSample{}, fmt.Errorf("server rejected upload")
	}
	if body.bytesRead != numBytes {
		return ThroughputSample{}, fmt.Errorf("HTTP transport consumed %d upload bytes; expected %d", body.bytesRead, numBytes)
	}
	if receipt.AcceptedBytes != numBytes {
		return ThroughputSample{}, fmt.Errorf("server accepted %d upload bytes; expected %d", receipt.AcceptedBytes, numBytes)
	}

	duration := time.Duration(receipt.ServerDurationNS)
	if duration <= 0 {
		duration = body.duration()
	}
	if duration <= 0 {
		return ThroughputSample{}, fmt.Errorf("invalid upload duration %s", duration)
	}

	mbps := float64(receipt.AcceptedBytes*8) / duration.Seconds() / 1e6
	return ThroughputSample{
		Timestamp: time.Now(), Direction: "upload", SizeBytes: receipt.AcceptedBytes,
		Duration: duration, Mbps: mbps, Profile: profileName, RunIndex: run,
	}, nil
}

// runPacketLossTest runs the WebRTC packet loss test.
func (c *Client) runPacketLossTest(ctx context.Context) (*PacketLossResult, error) {
	// Use WebRTC implementation with pion/webrtc
	return c.runPacketLossTestWebRTC(ctx)
}

// calculateSummary computes summary statistics from samples.
func (c *Client) calculateSummary(r *Results) Summary {
	var summary Summary

	// Download speed (p90)
	var dlSpeeds []float64
	for _, s := range r.ThroughputSamples {
		if s.Direction == "download" {
			dlSpeeds = append(dlSpeeds, s.Mbps)
		}
	}
	if len(dlSpeeds) > 0 {
		sort.Float64s(dlSpeeds)
		summary.DownloadMbps = percentile(dlSpeeds, 90)
	}

	// Upload speed (p90)
	var ulSpeeds []float64
	for _, s := range r.ThroughputSamples {
		if s.Direction == "upload" {
			ulSpeeds = append(ulSpeeds, s.Mbps)
		}
	}
	if len(ulSpeeds) > 0 {
		sort.Float64s(ulSpeeds)
		summary.UploadMbps = percentile(ulSpeeds, 90)
	}

	// Latency (median for unloaded, p90 for loaded)
	var unloadedLatencies []float64
	var downloadLatencies []float64
	var uploadLatencies []float64

	for _, s := range r.LatencySamples {
		ms := float64(s.RTT.Microseconds()) / 1000
		switch s.Phase {
		case "unloaded":
			unloadedLatencies = append(unloadedLatencies, ms)
		case "download":
			downloadLatencies = append(downloadLatencies, ms)
		case "upload":
			uploadLatencies = append(uploadLatencies, ms)
		}
	}

	if len(unloadedLatencies) > 0 {
		sort.Float64s(unloadedLatencies)
		summary.LatencyUnloadedMs = percentile(unloadedLatencies, 50)
		summary.JitterMs = percentile(unloadedLatencies, 90) - percentile(unloadedLatencies, 50)
	}

	if len(downloadLatencies) > 0 {
		sort.Float64s(downloadLatencies)
		summary.LatencyDownloadMs = percentile(downloadLatencies, 90)
	}

	if len(uploadLatencies) > 0 {
		sort.Float64s(uploadLatencies)
		summary.LatencyUploadMs = percentile(uploadLatencies, 90)
	}

	// Packet loss
	if r.PacketLoss != nil && !r.PacketLoss.Unavailable {
		loss := r.PacketLoss.LossPercent
		summary.PacketLossPercent = &loss
	}

	return summary
}

// percentile calculates the p-th percentile of sorted values.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100)
	return sorted[idx]
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
