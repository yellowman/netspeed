// Package client provides the speed test client implementation.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yellowman/netspeed/internal/measurement"
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

	// LegacyMaxTransferBytes is used when a third-party server does not
	// advertise the phase-1 measurement capabilities in /meta.
	LegacyMaxTransferBytes int64 = 100_000_000
	maxErrorBodyBytes      int64 = 4 << 10
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

func newMeasurementID() string {
	seq := measurementSequence.Add(1)
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), seq)
}

func requireStatusOK(resp *http.Response, operation string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return fmt.Errorf("%s returned HTTP %d and its error body could not be read: %w", operation, resp.StatusCode, err)
	}
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return fmt.Errorf("%s returned HTTP %d", operation, resp.StatusCode)
	}
	return fmt.Errorf("%s returned HTTP %d: %s", operation, resp.StatusCode, detail)
}

func (c *Client) endpoint(path string, query url.Values) (string, error) {
	u, err := url.Parse(strings.TrimRight(c.cfg.ServerURL, "/") + path)
	if err != nil {
		return "", fmt.Errorf("build %s endpoint: %w", path, err)
	}

	q := u.Query()
	for key, values := range query {
		for _, value := range values {
			q.Add(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// generatedUploadBody streams a deterministic zero-filled request body without
// retaining a payload-sized allocation. Its timestamps are protected because
// net/http may consume the body on a transport goroutine.
type generatedUploadBody struct {
	mu        sync.Mutex
	remaining int64
	readBytes int64
	firstRead time.Time
	lastRead  time.Time
}

func newGeneratedUploadBody(size int64) *generatedUploadBody {
	return &generatedUploadBody{remaining: size}
}

func (b *generatedUploadBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.remaining == 0 {
		return 0, io.EOF
	}

	n := int64(len(p))
	if n > b.remaining {
		n = b.remaining
	}
	clear(p[:int(n)])

	now := time.Now()
	if b.firstRead.IsZero() {
		b.firstRead = now
	}
	b.lastRead = now
	b.remaining -= n
	b.readBytes += n
	return int(n), nil
}

func (b *generatedUploadBody) Close() error { return nil }

func (b *generatedUploadBody) snapshot() (readBytes int64, firstRead, lastRead time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readBytes, b.firstRead, b.lastRead
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
	cfg                   Config
	httpClient            *http.Client
	maxTransferBytes      int64
	measurementAPIVersion int
}

var measurementSequence atomic.Uint64

// New creates a new speed test client.
func New(cfg Config) *Client {
	// Custom dialer with TCP optimizations
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return &Client{
		cfg:              cfg,
		maxTransferBytes: LegacyMaxTransferBytes,
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
		return nil, fmt.Errorf("download-only and upload-only cannot be used together")
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

	// Run packet loss test. A skipped or failed test remains explicitly
	// unavailable; it must never be converted into a perfect 0% result.
	if c.cfg.SkipPacketLoss {
		results.PacketLoss = &PacketLossResult{
			Unavailable: true,
			Reason:      "skipped by user",
		}
	} else {
		packetLoss, err := c.runPacketLossTest(ctx)
		if err != nil {
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
	endpoint, err := c.endpoint("/meta", nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	setRequestHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := requireStatusOK(resp, "metadata request"); err != nil {
		return nil, err
	}

	var meta Meta
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}

	// Reset negotiated capabilities on every run so reusing a Client against a
	// legacy server cannot retain a previous API-v1 contract.
	c.measurementAPIVersion = 0
	c.maxTransferBytes = LegacyMaxTransferBytes

	if meta.MeasurementAPIVersion >= measurement.APIVersion {
		if meta.MaxTransferBytes <= 0 {
			return nil, fmt.Errorf("measurement API v%d did not advertise a positive maxTransferBytes", meta.MeasurementAPIVersion)
		}
		c.measurementAPIVersion = meta.MeasurementAPIVersion
		c.maxTransferBytes = meta.MaxTransferBytes
	} else if meta.MaxTransferBytes > 0 {
		// Honor a capability-aware legacy server even if it does not yet
		// advertise the byte-counted receipt version.
		c.maxTransferBytes = meta.MaxTransferBytes
	}

	return &meta, nil
}

// runAdaptiveLatencyTest runs latency probes with adaptive batching (matching web client).
func (c *Client) runAdaptiveLatencyTest(ctx context.Context, phase string, count int) ([]LatencySample, error) {
	if c.cfg.Quick {
		count = 5
	}
	if count <= 0 {
		return nil, fmt.Errorf("latency test requires at least one probe")
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

	// Phase 2: Decide batching strategy based on median RTT
	medianRTT := c.calculateMedianRTT(samples)
	useParallel := false

	if len(samples) > 0 {
		if medianRTT < LowLatencyThreshold {
			useParallel = true
		} else if medianRTT >= HighLatencyThreshold {
			// High latency: check bandwidth to distinguish satellite from slow DSL.
			bandwidth := c.quickBandwidthEstimate(ctx)
			useParallel = bandwidth >= MinBandwidthForParallel
		} else {
			useParallel = true // 50-100ms range
		}
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

	minimum := 3
	if count < minimum {
		minimum = count
	}
	if len(samples) < minimum {
		return samples, fmt.Errorf("only %d of %d latency probes succeeded; need at least %d", len(samples), count, minimum)
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
	const preferredBytes int64 = 100_000
	bytesToRequest := preferredBytes
	if c.maxTransferBytes < bytesToRequest {
		bytesToRequest = c.maxTransferBytes
	}
	if bytesToRequest <= 0 {
		return 0
	}

	endpoint, err := c.endpoint("/__down", url.Values{
		"bytes":  {strconv.FormatInt(bytesToRequest, 10)},
		"measId": {"bandwidth-estimate-" + newMeasurementID()},
	})
	if err != nil {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0
	}
	setRequestHeaders(req)
	req.Header.Set("Cache-Control", "no-store")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if err := requireStatusOK(resp, "bandwidth estimate"); err != nil {
		return 0
	}
	if resp.ContentLength >= 0 && resp.ContentLength != bytesToRequest {
		return 0
	}

	received, err := io.Copy(io.Discard, resp.Body)
	if err != nil || received != bytesToRequest {
		return 0
	}

	duration := time.Since(start)
	if duration <= 0 {
		return 0
	}
	return float64(received*8) / duration.Seconds() / 1e6
}

// measureLatency measures a single latency probe using precise timing.
// RTT = GotFirstResponseByte - WroteRequest (excludes connection setup, TLS, DNS)
func (c *Client) measureLatency(ctx context.Context, phase string, seq int) (time.Duration, error) {
	endpoint, err := c.endpoint("/__down", url.Values{
		"bytes":  {"0"},
		"measId": {newMeasurementID()},
		"during": {phase},
		"seq":    {strconv.Itoa(seq)},
	})
	if err != nil {
		return 0, err
	}

	// Set up precise timing via httptrace
	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
	if err := requireStatusOK(resp, "latency probe"); err != nil {
		return 0, err
	}
	if resp.ContentLength > 0 {
		return 0, fmt.Errorf("latency probe returned Content-Length %d; expected 0", resp.ContentLength)
	}
	received, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read latency response: %w", err)
	}
	if received != 0 {
		return 0, fmt.Errorf("latency probe returned %d body bytes; expected 0", received)
	}

	// RTT = time from request written to first response byte
	// This excludes connection setup, TLS handshake, and DNS resolution
	if timing.wroteRequest.IsZero() || timing.gotFirstByte.IsZero() {
		// Fallback if trace didn't fire (shouldn't happen)
		return 0, fmt.Errorf("timing trace failed")
	}

	rtt := timing.gotFirstByte.Sub(timing.wroteRequest)
	if rtt <= 0 {
		return 0, fmt.Errorf("latency timing was not positive")
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
	selected := profilesWithinLimit(baseline, maxBytes)

	// Add larger profiles based on estimated transfer time
	for _, p := range allProfiles {
		if p.Bytes <= 0 || p.Bytes > maxBytes {
			continue
		}
		// Skip baseline profiles (already included)
		if profileIn(p, baseline) {
			continue
		}

		if estimateTransferTime(p.Bytes, estimatedSpeed) <= MaxTestDuration {
			selected = append(selected, p)
		}
	}

	return selected
}

func profilesWithinLimit(profiles []profile, maxBytes int64) []profile {
	selected := make([]profile, 0, len(profiles))
	for _, p := range profiles {
		if p.Bytes > 0 && p.Bytes <= maxBytes {
			selected = append(selected, p)
		}
	}
	return selected
}

func profileIn(candidate profile, profiles []profile) bool {
	for _, p := range profiles {
		if candidate.Name == p.Name {
			return true
		}
	}
	return false
}

func minimumValidRuns(p profile) int {
	if p.Runs >= 3 {
		return 2
	}
	return 1
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

	var samples []ThroughputSample
	var loadedLatency []LatencySample
	phaseStart := time.Now()

	// Phase 1: Run baseline profiles
	baselineProfiles := profilesWithinLimit(baselineDownloadProfiles, c.maxTransferBytes)
	if len(baselineProfiles) == 0 {
		return nil, nil, fmt.Errorf("server transfer limit %d is below the smallest download profile", c.maxTransferBytes)
	}
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
		if profileIn(p, baselineProfiles) {
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

	var samples []ThroughputSample
	var loadedLatency []LatencySample
	phaseStart := time.Now()

	// Phase 1: Run baseline profiles
	baselineProfiles := profilesWithinLimit(baselineUploadProfiles, c.maxTransferBytes)
	if len(baselineProfiles) == 0 {
		return nil, nil, fmt.Errorf("server transfer limit %d is below the smallest upload profile", c.maxTransferBytes)
	}
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
		if profileIn(p, baselineProfiles) {
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
		if p.Bytes <= 0 || p.Bytes > c.maxTransferBytes {
			return samples, fmt.Errorf("download profile %s requests %d bytes; server maximum is %d", p.Name, p.Bytes, c.maxTransferBytes)
		}

		validRuns := 0
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

			samples = append(samples, sample)
			validRuns++
			currentRun++

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress(direction, currentRun, totalRuns, sample.Mbps)
			}
		}

		minimum := minimumValidRuns(p)
		if validRuns < minimum {
			if lastErr == nil {
				lastErr = fmt.Errorf("no measurement error was reported")
			}
			return samples, fmt.Errorf("download profile %s produced %d valid samples; need at least %d: %w", p.Name, validRuns, minimum, lastErr)
		}
	}

	return samples, nil
}

// measureDownload measures a single download using precise timing.
// Body transfer time = bodyDone - GotFirstResponseByte (excludes connection, TLS, headers)
func (c *Client) measureDownload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	if numBytes < 0 || numBytes > c.maxTransferBytes {
		return ThroughputSample{}, fmt.Errorf("download request size %d exceeds server maximum %d", numBytes, c.maxTransferBytes)
	}

	endpoint, err := c.endpoint("/__down", url.Values{
		"bytes":   {strconv.FormatInt(numBytes, 10)},
		"measId":  {newMeasurementID()},
		"profile": {profileName},
		"run":     {strconv.Itoa(run)},
	})
	if err != nil {
		return ThroughputSample{}, err
	}

	// Set up precise timing via httptrace
	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ThroughputSample{}, err
	}
	setRequestHeaders(req)
	req.Header.Set("Cache-Control", "no-store")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()
	if err := requireStatusOK(resp, "download measurement"); err != nil {
		return ThroughputSample{}, err
	}
	if resp.ContentLength >= 0 && resp.ContentLength != numBytes {
		return ThroughputSample{}, fmt.Errorf("download Content-Length was %d; expected %d", resp.ContentLength, numBytes)
	}

	// Read body - timing.gotFirstByte is set when first byte arrives
	received, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return ThroughputSample{}, fmt.Errorf("read download body: %w", err)
	}
	if received != numBytes {
		return ThroughputSample{}, fmt.Errorf("download returned %d bytes; expected %d", received, numBytes)
	}
	bodyDone := time.Now()

	// Body transfer time only (excludes connection setup, TLS, headers)
	var duration time.Duration
	if timing.gotFirstByte.IsZero() {
		return ThroughputSample{}, fmt.Errorf("download timing trace did not record the first response byte")
	}
	duration = bodyDone.Sub(timing.gotFirstByte)

	if duration <= 0 {
		return ThroughputSample{}, fmt.Errorf("download body duration was not positive")
	}

	mbps := float64(received*8) / duration.Seconds() / 1e6

	return ThroughputSample{
		Timestamp: time.Now(),
		Direction: "download",
		SizeBytes: received,
		Duration:  duration,
		Mbps:      mbps,
		Profile:   profileName,
		RunIndex:  run,
	}, nil
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
		if p.Bytes <= 0 || p.Bytes > c.maxTransferBytes {
			return samples, fmt.Errorf("upload profile %s requests %d bytes; server maximum is %d", p.Name, p.Bytes, c.maxTransferBytes)
		}

		validRuns := 0
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

			samples = append(samples, sample)
			validRuns++
			currentRun++

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("upload", currentRun, totalRuns, sample.Mbps)
			}
		}

		minimum := minimumValidRuns(p)
		if validRuns < minimum {
			if lastErr == nil {
				lastErr = fmt.Errorf("no measurement error was reported")
			}
			return samples, fmt.Errorf("upload profile %s produced %d valid samples; need at least %d: %w", p.Name, validRuns, minimum, lastErr)
		}
	}

	return samples, nil
}

// measureUpload measures a single upload. Measurement API v1 uses the
// server-side body-read duration and verifies the exact accepted byte count.
func (c *Client) measureUpload(ctx context.Context, profileName string, numBytes int64, run int) (ThroughputSample, error) {
	if numBytes < 0 || numBytes > c.maxTransferBytes {
		return ThroughputSample{}, fmt.Errorf("upload request size %d exceeds server maximum %d", numBytes, c.maxTransferBytes)
	}

	endpoint, err := c.endpoint("/__up", url.Values{
		"measId":  {newMeasurementID()},
		"profile": {profileName},
		"run":     {strconv.Itoa(run)},
	})
	if err != nil {
		return ThroughputSample{}, err
	}

	// Set up precise timing via httptrace
	var timing timingInfo
	trace := createTrace(&timing)
	ctx = httptrace.WithClientTrace(ctx, trace)

	body := newGeneratedUploadBody(numBytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return ThroughputSample{}, err
	}
	setRequestHeaders(req)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Cache-Control", "no-store")
	req.ContentLength = numBytes

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()
	if err := requireStatusOK(resp, "upload measurement"); err != nil {
		return ThroughputSample{}, err
	}

	readBytes, firstRead, _ := body.snapshot()
	if readBytes != numBytes {
		return ThroughputSample{}, fmt.Errorf("HTTP transport consumed %d upload bytes; expected %d", readBytes, numBytes)
	}

	var duration time.Duration
	if c.measurementAPIVersion >= measurement.APIVersion {
		receipt, err := measurement.DecodeUploadReceipt(resp.Body)
		if err != nil {
			return ThroughputSample{}, fmt.Errorf("decode upload receipt: %w", err)
		}
		if receipt.AcceptedBytes != numBytes {
			return ThroughputSample{}, fmt.Errorf("server accepted %d upload bytes; expected %d", receipt.AcceptedBytes, numBytes)
		}
		duration = time.Duration(receipt.ServerDurationNS)
	} else {
		if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, measurement.MaxReceiptBytes)); err != nil {
			return ThroughputSample{}, fmt.Errorf("read upload response: %w", err)
		}
		if firstRead.IsZero() || timing.gotFirstByte.IsZero() {
			return ThroughputSample{}, fmt.Errorf("legacy upload timing trace was incomplete")
		}
		duration = timing.gotFirstByte.Sub(firstRead)
	}

	if duration <= 0 {
		return ThroughputSample{}, fmt.Errorf("upload duration was not positive")
	}

	mbps := float64(numBytes*8) / duration.Seconds() / 1e6

	return ThroughputSample{
		Timestamp: time.Now(),
		Direction: "upload",
		SizeBytes: numBytes,
		Duration:  duration,
		Mbps:      mbps,
		Profile:   profileName,
		RunIndex:  run,
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
		summary.DownloadMbps = float64Pointer(percentile(dlSpeeds, 90))
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
		summary.UploadMbps = float64Pointer(percentile(ulSpeeds, 90))
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
		summary.LatencyUnloadedMs = float64Pointer(percentile(unloadedLatencies, 50))
		summary.JitterMs = float64Pointer(percentile(unloadedLatencies, 90) - percentile(unloadedLatencies, 50))
	}

	if len(downloadLatencies) > 0 {
		sort.Float64s(downloadLatencies)
		summary.LatencyDownloadMs = float64Pointer(percentile(downloadLatencies, 90))
	}

	if len(uploadLatencies) > 0 {
		sort.Float64s(uploadLatencies)
		summary.LatencyUploadMs = float64Pointer(percentile(uploadLatencies, 90))
	}

	// Packet loss
	if r.PacketLoss != nil && !r.PacketLoss.Unavailable {
		summary.PacketLossPercent = float64Pointer(r.PacketLoss.LossPercent)
	}

	return summary
}

func float64Pointer(value float64) *float64 {
	return &value
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
	if s.DownloadMbps == nil || s.LatencyUnloadedMs == nil || s.JitterMs == nil || s.PacketLossPercent == nil {
		return "N/A"
	}

	if *s.DownloadMbps >= 50 && *s.LatencyUnloadedMs <= 25 &&
		*s.JitterMs <= 5 && *s.PacketLossPercent <= 0.5 {
		return "Great"
	}
	if *s.DownloadMbps >= 20 && *s.LatencyUnloadedMs <= 50 &&
		*s.JitterMs <= 15 && *s.PacketLossPercent <= 1.5 {
		return "Good"
	}
	if *s.DownloadMbps >= 10 && *s.LatencyUnloadedMs <= 80 &&
		*s.JitterMs <= 30 && *s.PacketLossPercent <= 3 {
		return "Okay"
	}
	return "Poor"
}

func gradeForGaming(s Summary) string {
	if s.DownloadMbps == nil || s.LatencyUnloadedMs == nil || s.JitterMs == nil || s.PacketLossPercent == nil {
		return "N/A"
	}

	if *s.DownloadMbps >= 25 && *s.LatencyUnloadedMs <= 20 &&
		*s.JitterMs <= 5 && *s.PacketLossPercent <= 0.1 {
		return "Great"
	}
	if *s.DownloadMbps >= 15 && *s.LatencyUnloadedMs <= 40 &&
		*s.JitterMs <= 10 && *s.PacketLossPercent <= 0.5 {
		return "Good"
	}
	if *s.DownloadMbps >= 5 && *s.LatencyUnloadedMs <= 80 &&
		*s.JitterMs <= 20 && *s.PacketLossPercent <= 1 {
		return "Okay"
	}
	return "Poor"
}

func gradeForVideoChat(s Summary) string {
	if s.DownloadMbps == nil || s.UploadMbps == nil || s.LatencyUnloadedMs == nil || s.JitterMs == nil || s.PacketLossPercent == nil {
		return "N/A"
	}

	if *s.DownloadMbps >= 10 && *s.UploadMbps >= 5 &&
		*s.LatencyUnloadedMs <= 50 &&
		*s.JitterMs <= 10 && *s.PacketLossPercent <= 1 {
		return "Great"
	}
	if *s.DownloadMbps >= 5 && *s.UploadMbps >= 2 &&
		*s.LatencyUnloadedMs <= 100 &&
		*s.JitterMs <= 20 && *s.PacketLossPercent <= 2 {
		return "Good"
	}
	if *s.DownloadMbps >= 2 && *s.UploadMbps >= 1 &&
		*s.LatencyUnloadedMs <= 150 &&
		*s.JitterMs <= 40 && *s.PacketLossPercent <= 5 {
		return "Okay"
	}
	return "Poor"
}
