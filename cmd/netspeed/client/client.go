// Package client provides the speed test client implementation.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

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
			Timeout: 30 * time.Second,
		},
	}
}

// Run executes the full speed test suite.
func (c *Client) Run(ctx context.Context) (*Results, error) {
	results := &Results{
		Timestamp: time.Now(),
		ServerURL: c.cfg.ServerURL,
	}

	// Fetch metadata
	meta, err := c.fetchMeta(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	results.Meta = meta

	// Run latency tests (unloaded)
	latencySamples, err := c.runLatencyTest(ctx, "unloaded", 20)
	if err != nil {
		return nil, fmt.Errorf("latency test failed: %w", err)
	}
	results.LatencySamples = append(results.LatencySamples, latencySamples...)

	// Run download tests
	if !c.cfg.UploadOnly {
		downloadSamples, err := c.runDownloadTests(ctx)
		if err != nil {
			return nil, fmt.Errorf("download test failed: %w", err)
		}
		results.ThroughputSamples = append(results.ThroughputSamples, downloadSamples...)
	}

	// Run upload tests
	if !c.cfg.DownloadOnly {
		uploadSamples, err := c.runUploadTests(ctx)
		if err != nil {
			return nil, fmt.Errorf("upload test failed: %w", err)
		}
		results.ThroughputSamples = append(results.ThroughputSamples, uploadSamples...)
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

// runLatencyTest runs latency probes.
func (c *Client) runLatencyTest(ctx context.Context, phase string, count int) ([]LatencySample, error) {
	if c.cfg.Quick {
		count = 5
	}

	samples := make([]LatencySample, 0, count)

	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return samples, ctx.Err()
		default:
		}

		rtt, err := c.measureLatency(ctx, phase, i)
		if err != nil {
			continue // Skip failed probes
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

	return samples, nil
}

// measureLatency measures a single latency probe.
func (c *Client) measureLatency(ctx context.Context, phase string, seq int) (time.Duration, error) {
	url := fmt.Sprintf("%s/__down?bytes=0&phase=%s&seq=%d", c.cfg.ServerURL, phase, seq)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Cache-Control", "no-store")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	rtt := time.Since(start)

	return rtt, nil
}

// Download profile configuration
type downloadProfile struct {
	Name  string
	Bytes int64
	Runs  int
}

var downloadProfiles = []downloadProfile{
	{"100kB", 100_000, 10},
	{"1MB", 1_000_000, 8},
	{"10MB", 10_000_000, 6},
	{"25MB", 25_000_000, 4},
	{"100MB", 100_000_000, 3},
}

var quickDownloadProfiles = []downloadProfile{
	{"100kB", 100_000, 3},
	{"1MB", 1_000_000, 3},
}

// runDownloadTests runs download speed tests.
func (c *Client) runDownloadTests(ctx context.Context) ([]ThroughputSample, error) {
	profiles := downloadProfiles
	if c.cfg.Quick {
		profiles = quickDownloadProfiles
	}

	var samples []ThroughputSample
	totalRuns := 0
	for _, p := range profiles {
		totalRuns += p.Runs
	}

	currentRun := 0
	for _, profile := range profiles {
		for run := 0; run < profile.Runs; run++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureDownload(ctx, profile.Name, profile.Bytes, run)
			if err != nil {
				continue // Skip failed downloads
			}

			samples = append(samples, sample)
			currentRun++

			if c.cfg.OnProgress != nil {
				c.cfg.OnProgress("download", currentRun, totalRuns, sample.Mbps)
			}
		}
	}

	return samples, nil
}

// measureDownload measures a single download.
func (c *Client) measureDownload(ctx context.Context, profile string, bytes int64, run int) (ThroughputSample, error) {
	url := fmt.Sprintf("%s/__down?bytes=%d&profile=%s&run=%d", c.cfg.ServerURL, bytes, profile, run)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ThroughputSample{}, err
	}
	req.Header.Set("Cache-Control", "no-store")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()

	received, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return ThroughputSample{}, err
	}

	duration := time.Since(start)
	mbps := float64(received*8) / duration.Seconds() / 1e6

	return ThroughputSample{
		Timestamp:  time.Now(),
		Direction:  "download",
		SizeBytes:  received,
		Duration:   duration,
		Mbps:       mbps,
		Profile:    profile,
		RunIndex:   run,
	}, nil
}

// Upload profile configuration
type uploadProfile struct {
	Name  string
	Bytes int64
	Runs  int
}

var uploadProfiles = []uploadProfile{
	{"100kB", 100_000, 8},
	{"1MB", 1_000_000, 6},
	{"10MB", 10_000_000, 4},
	{"25MB", 25_000_000, 4},
	{"50MB", 50_000_000, 3},
}

var quickUploadProfiles = []uploadProfile{
	{"100kB", 100_000, 3},
	{"1MB", 1_000_000, 3},
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

// runUploadTests runs upload speed tests.
func (c *Client) runUploadTests(ctx context.Context) ([]ThroughputSample, error) {
	profiles := uploadProfiles
	if c.cfg.Quick {
		profiles = quickUploadProfiles
	}

	var samples []ThroughputSample
	totalRuns := 0
	for _, p := range profiles {
		totalRuns += p.Runs
	}

	currentRun := 0
	for _, profile := range profiles {
		payload := getPayload(profile.Bytes)

		for run := 0; run < profile.Runs; run++ {
			select {
			case <-ctx.Done():
				return samples, ctx.Err()
			default:
			}

			sample, err := c.measureUpload(ctx, profile.Name, payload, run)
			if err != nil {
				continue // Skip failed uploads
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

// measureUpload measures a single upload.
func (c *Client) measureUpload(ctx context.Context, profile string, payload []byte, run int) (ThroughputSample, error) {
	url := fmt.Sprintf("%s/__up?profile=%s&run=%d", c.cfg.ServerURL, profile, run)

	start := time.Now()
	resp, err := c.httpClient.Post(url, "application/octet-stream", &payloadReader{data: payload})
	if err != nil {
		return ThroughputSample{}, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	duration := time.Since(start)
	mbps := float64(len(payload)*8) / duration.Seconds() / 1e6

	return ThroughputSample{
		Timestamp:  time.Now(),
		Direction:  "upload",
		SizeBytes:  int64(len(payload)),
		Duration:   duration,
		Mbps:       mbps,
		Profile:    profile,
		RunIndex:   run,
	}, nil
}

// payloadReader implements io.Reader for upload payloads.
type payloadReader struct {
	data []byte
	pos  int
}

func (r *payloadReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// runPacketLossTest runs the WebRTC packet loss test.
func (c *Client) runPacketLossTest(ctx context.Context) (*PacketLossResult, error) {
	// For now, return unavailable since WebRTC requires pion/webrtc
	// This would be implemented with pion/webrtc in a full implementation
	return &PacketLossResult{
		Unavailable: true,
		Reason:      "WebRTC packet loss test not yet implemented in CLI",
	}, nil
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

	// Latency (median for unloaded)
	var unloadedLatencies []float64
	for _, s := range r.LatencySamples {
		if s.Phase == "unloaded" {
			unloadedLatencies = append(unloadedLatencies, float64(s.RTT.Microseconds())/1000)
		}
	}
	if len(unloadedLatencies) > 0 {
		sort.Float64s(unloadedLatencies)
		summary.LatencyUnloadedMs = percentile(unloadedLatencies, 50)
		summary.JitterMs = percentile(unloadedLatencies, 90) - percentile(unloadedLatencies, 50)
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
		OnlineGaming:   gradeForGaming(s),
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
