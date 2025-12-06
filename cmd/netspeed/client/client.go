// Package client provides the speed test client implementation.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"sync"
	"time"
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
	ReadBufferSize  = 4 * 1024 * 1024 // 4MB read buffer
	WriteBufferSize = 4 * 1024 * 1024 // 4MB write buffer
)

// Time budget constants (matching web client)
const (
	MaxTestDuration     = 4 * time.Second  // Max time for single profile to be selected
	TotalPhaseBudget    = 8 * time.Second  // Total time budget per phase
	LowLatencyThreshold = 50 * time.Millisecond
	HighLatencyThreshold = 100 * time.Millisecond
	MinBandwidthForParallel = 2.0 // Mbps
)

// Client performs speed tests.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// New creates a new speed test client.
func New(cfg Config) *Client {
	// Custom dialer with TCP optimizations
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					conn, err := dialer.DialContext(ctx, network, addr)
					if err != nil {
						return nil, err
					}

					// Set TCP options for high-speed transfers
					if tcpConn, ok := conn.(*net.TCPConn); ok {
						tcpConn.SetNoDelay(true)                    // Disable Nagle's algorithm
						tcpConn.SetReadBuffer(ReadBufferSize)       // 4MB read buffer
						tcpConn.SetWriteBuffer(WriteBufferSize)     // 4MB write buffer
					}

					return conn, nil
				},
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   100,
				MaxConnsPerHost:       100,
				IdleConnTimeout:       90 * time.Second,
				DisableCompression:    true, // Important for accurate bandwidth measurement
				ForceAttemptHTTP2:     true,
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
	url := c.cfg.ServerURL + "/meta"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var meta Meta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
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
		return samples, nil
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

// quickBandwidthEstimate does a quick 100KB download to estimate bandwidth.
func (c *Client) quickBandwidthEstimate(ctx context.Context) float64 {
	url := fmt.Sprintf("%s/__down?bytes=100000", c.cfg.ServerURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Cache-Control", "no-store")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	received, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0
	}

	duration := time.Since(start)
	return float64(received*8) / duration.Seconds() / 1e6
}

// measureLatency measures a single latency probe using precise timing.
// RTT = GotFirstResponseByte - WroteRequest (excludes connection setup, TLS, DNS)
func (c *Client) measureLatency(ctx context.Context, phase string, seq int) (time.Duration, error) {
	url := fmt.Sprintf("%s/__down?bytes=0&phase=%s&seq=%d", c.cfg.ServerURL, phase, seq)

	// Set up precise timing via httptrace
	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Cache-Control", "no-store")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// RTT = time from request written to first response byte
	// This excludes connection setup, TLS handshake, and DNS resolution
	if timing.wroteRequest.IsZero() || timing.gotFirstByte.IsZero() {
		// Fallback if trace didn't fire (shouldn't happen)
		return 0, fmt.Errorf("timing trace failed")
	}

	rtt := timing.gotFirstByte.Sub(timing.wroteRequest)
	return rtt, nil
}

// Profile configuration
type profile struct {
	Name  string
	Bytes int64
	Runs  int
}

// All download profiles matching web client
var allDownloadProfiles = []profile{
	{"100kB", 100_000, 10},
	{"1MB", 1_000_000, 8},
	{"10MB", 10_000_000, 6},
	{"25MB", 25_000_000, 4},
	{"100MB", 100_000_000, 3},
	{"250MB", 250_000_000, 2},
	{"500MB", 500_000_000, 2},
	{"1GB", 1_000_000_000, 2},
}

// All upload profiles matching web client
var allUploadProfiles = []profile{
	{"100kB", 100_000, 8},
	{"1MB", 1_000_000, 6},
	{"10MB", 10_000_000, 4},
	{"25MB", 25_000_000, 4},
	{"50MB", 50_000_000, 3},
	{"100MB", 100_000_000, 2},
	{"250MB", 250_000_000, 2},
	{"500MB", 500_000_000, 2},
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
func selectProfiles(estimatedSpeed float64, allProfiles []profile, baseline []profile) []profile {
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

		if estimateTransferTime(p.Bytes, estimatedSpeed) <= MaxTestDuration {
			selected = append(selected, p)
		}
	}

	return selected
}

// runDownloadTests runs download speed tests with adaptive profile selection.
func (c *Client) runDownloadTests(ctx context.Context) ([]ThroughputSample, []LatencySample, error) {
	if c.cfg.Quick {
		samples, err := c.runProfiles(ctx, quickDownloadProfiles, "download")
		return samples, nil, err
	}

	var samples []ThroughputSample
	var loadedLatency []LatencySample
	phaseStart := time.Now()

	// Phase 1: Run baseline profiles
	baselineSamples, err := c.runProfiles(ctx, baselineDownloadProfiles, "download")
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
	selectedProfiles := selectProfiles(estimatedSpeed, allDownloadProfiles, baselineDownloadProfiles)

	for _, p := range selectedProfiles {
		// Skip baseline (already run)
		isBaseline := false
		for _, b := range baselineDownloadProfiles {
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
		samples, err := c.runUploadProfiles(ctx, quickUploadProfiles)
		return samples, nil, err
	}

	var samples []ThroughputSample
	var loadedLatency []LatencySample
	phaseStart := time.Now()

	// Phase 1: Run baseline profiles
	baselineSamples, err := c.runUploadProfiles(ctx, baselineUploadProfiles)
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
	selectedProfiles := selectProfiles(estimatedSpeed, allUploadProfiles, baselineUploadProfiles)

	for _, p := range selectedProfiles {
		// Skip baseline (already run)
		isBaseline := false
		for _, b := range baselineUploadProfiles {
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
	for i := 0; i < count; i++ {
		rtt, err := c.measureLatency(ctx, phase, i)
		if err != nil {
			continue
		}
		samples = append(samples, LatencySample{
			Timestamp: time.Now(),
			RTT:       rtt,
			Phase:     phase,
		})
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
		for run := 0; run < p.Runs; run++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureDownload(ctx, p.Name, p.Bytes, run)
			if err != nil {
				continue
			}

			samples = append(samples, sample)
			currentRun++

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress(direction, currentRun, totalRuns, sample.Mbps)
			}
		}
	}

	return samples, nil
}

// measureDownload measures a single download using precise timing.
// Body transfer time = bodyDone - GotFirstResponseByte (excludes connection, TLS, headers)
func (c *Client) measureDownload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	url := fmt.Sprintf("%s/__down?bytes=%d&profile=%s&run=%d", c.cfg.ServerURL, numBytes, profileName, run)

	// Set up precise timing via httptrace
	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ThroughputSample{}, err
	}
	req.Header.Set("Cache-Control", "no-store")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()

	// Read body - timing.gotFirstByte is set when first byte arrives
	received, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return ThroughputSample{}, err
	}
	bodyDone := time.Now()

	// Body transfer time only (excludes connection setup, TLS, headers)
	var duration time.Duration
	if !timing.gotFirstByte.IsZero() {
		duration = bodyDone.Sub(timing.gotFirstByte)
	} else {
		// Fallback to total time if trace didn't fire
		duration = bodyDone.Sub(timing.wroteRequest)
	}

	// Protect against zero/negative duration
	if duration <= 0 {
		duration = time.Millisecond
	}

	mbps := float64(received*8) / duration.Seconds() / 1e6

	return ThroughputSample{
		Timestamp:  time.Now(),
		Direction:  "download",
		SizeBytes:  received,
		Duration:   duration,
		Mbps:       mbps,
		Profile:    profileName,
		RunIndex:   run,
	}, nil
}

// Payload cache for uploads
var (
	payloadCache = make(map[int64][]byte)
	payloadMu    sync.Mutex
)

func getPayload(size int64) []byte {
	payloadMu.Lock()
	defer payloadMu.Unlock()

	if payload, ok := payloadCache[size]; ok {
		return payload
	}

	payload := make([]byte, size)
	// Zero-filled is fine for bandwidth measurement
	payloadCache[size] = payload

	return payload
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
		payload := getPayload(p.Bytes)

		for run := 0; run < p.Runs; run++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureUpload(ctx, p.Name, payload, run)
			if err != nil {
				continue
			}

			samples = append(samples, sample)
			currentRun++

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("upload", currentRun, totalRuns, sample.Mbps)
			}
		}
	}

	return samples, nil
}

// measureUpload measures a single upload using precise timing.
// Upload time = GotFirstResponseByte - bodyWriteStart
func (c *Client) measureUpload(ctx context.Context, profileName string, payload []byte, run int) (ThroughputSample, error) {
	url := fmt.Sprintf("%s/__up?profile=%s&run=%d", c.cfg.ServerURL, profileName, run)

	// Set up precise timing via httptrace
	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	// Track when body write starts
	bodyWriteStart := time.Now()

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return ThroughputSample{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(payload))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// Upload time = time from body write start to response received
	var duration time.Duration
	if !timing.gotFirstByte.IsZero() {
		duration = timing.gotFirstByte.Sub(bodyWriteStart)
	} else {
		// Fallback
		duration = time.Since(bodyWriteStart)
	}

	// Protect against zero/negative duration
	if duration <= 0 {
		duration = time.Millisecond
	}

	mbps := float64(len(payload)*8) / duration.Seconds() / 1e6

	return ThroughputSample{
		Timestamp:  time.Now(),
		Direction:  "upload",
		SizeBytes:  int64(len(payload)),
		Duration:   duration,
		Mbps:       mbps,
		Profile:    profileName,
		RunIndex:   run,
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
		summary.PacketLossPercent = r.PacketLoss.LossPercent
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
	if s.DownloadMbps >= 50 && s.LatencyUnloadedMs <= 25 &&
		s.JitterMs <= 5 && s.PacketLossPercent <= 0.5 {
		return "Great"
	}
	if s.DownloadMbps >= 20 && s.LatencyUnloadedMs <= 50 &&
		s.JitterMs <= 15 && s.PacketLossPercent <= 1.5 {
		return "Good"
	}
	if s.DownloadMbps >= 10 && s.LatencyUnloadedMs <= 80 &&
		s.JitterMs <= 30 && s.PacketLossPercent <= 3 {
		return "Okay"
	}
	return "Poor"
}

func gradeForGaming(s Summary) string {
	if s.DownloadMbps >= 25 && s.LatencyUnloadedMs <= 20 &&
		s.JitterMs <= 5 && s.PacketLossPercent <= 0.1 {
		return "Great"
	}
	if s.DownloadMbps >= 15 && s.LatencyUnloadedMs <= 40 &&
		s.JitterMs <= 10 && s.PacketLossPercent <= 0.5 {
		return "Good"
	}
	if s.DownloadMbps >= 5 && s.LatencyUnloadedMs <= 80 &&
		s.JitterMs <= 20 && s.PacketLossPercent <= 1 {
		return "Okay"
	}
	return "Poor"
}

func gradeForVideoChat(s Summary) string {
	if s.DownloadMbps >= 10 && s.UploadMbps >= 5 &&
		s.LatencyUnloadedMs <= 50 &&
		s.JitterMs <= 10 && s.PacketLossPercent <= 1 {
		return "Great"
	}
	if s.DownloadMbps >= 5 && s.UploadMbps >= 2 &&
		s.LatencyUnloadedMs <= 100 &&
		s.JitterMs <= 20 && s.PacketLossPercent <= 2 {
		return "Good"
	}
	if s.DownloadMbps >= 2 && s.UploadMbps >= 1 &&
		s.LatencyUnloadedMs <= 150 &&
		s.JitterMs <= 40 && s.PacketLossPercent <= 5 {
		return "Okay"
	}
	return "Poor"
}
