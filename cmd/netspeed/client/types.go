package client

import (
	"encoding/json"
	"time"

	"github.com/yellowman/netspeed/internal/measurementhttp"
)

// Meta holds server and client metadata (matches web client format).
type Meta struct {
	Hostname                        string                        `json:"hostname"`
	ClientIP                        string                        `json:"clientIp"`
	HTTPProtocol                    string                        `json:"httpProtocol"`
	ASN                             int                           `json:"asn"`
	ASOrganization                  string                        `json:"asOrganization"`
	Colo                            string                        `json:"colo"`
	Country                         string                        `json:"country"`
	City                            string                        `json:"city"`
	Region                          string                        `json:"region"`
	PostalCode                      string                        `json:"postalCode"`
	Latitude                        float64                       `json:"latitude"`
	Longitude                       float64                       `json:"longitude"`
	Timezone                        string                        `json:"timezone,omitempty"`
	MaxTransferBytes                int64                         `json:"maxTransferBytes,omitempty"`
	MaxConcurrentTransfersPerClient int                           `json:"maxConcurrentTransfersPerClient,omitempty"`
	MeasurementProtocolVersion      int                           `json:"measurementProtocolVersion,omitempty"`
	UploadReceiptVersion            int                           `json:"uploadReceiptVersion,omitempty"`
	PacketLossFrameVersion          int                           `json:"packetLossFrameVersion,omitempty"`
	MeasurementCapabilities         *measurementhttp.Capabilities `json:"measurementCapabilities,omitempty"`
	MeasurementSelection            *measurementhttp.Selection    `json:"measurementSelection,omitempty"`
}

// LatencySample represents a single latency measurement (internal format).
type LatencySample struct {
	Timestamp            time.Time     `json:"-"`
	StartedAt            time.Time     `json:"-"`
	EndedAt              time.Time     `json:"-"`
	RTT                  time.Duration `json:"-"`
	Condition            string        `json:"-"` // "unloaded", "download", "upload"
	LoadOverlapped       bool          `json:"-"`
	LoadTrackingAccurate bool          `json:"-"`
	TimingSource         string        `json:"-"`
	ConnectionReused     bool          `json:"-"`
	ProbeTransport       string        `json:"-"`
	ProbeMethod          string        `json:"-"`
	ProbePath            string        `json:"-"`
}

// LatencySampleJSON is the JSON format matching the web client.
type LatencySampleJSON struct {
	Ts                   int64   `json:"ts"`
	StartedAt            int64   `json:"startedAt,omitempty"`
	EndedAt              int64   `json:"endedAt,omitempty"`
	RttMs                float64 `json:"rttMs"`
	Condition            string  `json:"condition"`
	LoadOverlapped       bool    `json:"loadOverlapped,omitempty"`
	LoadTrackingAccurate bool    `json:"loadTrackingAccurate,omitempty"`
	TimingSource         string  `json:"timingSource,omitempty"`
	ConnectionReused     bool    `json:"connectionReused"`
	ProbeTransport       string  `json:"probeTransport,omitempty"`
	ProbeMethod          string  `json:"probeMethod,omitempty"`
	ProbePath            string  `json:"probePath,omitempty"`
}

// ToJSON converts LatencySample to JSON format.
func (s LatencySample) ToJSON() LatencySampleJSON {
	out := LatencySampleJSON{
		Ts:                   s.Timestamp.UnixMilli(),
		RttMs:                float64(s.RTT.Microseconds()) / 1000.0,
		Condition:            s.Condition,
		LoadOverlapped:       s.LoadOverlapped,
		LoadTrackingAccurate: s.LoadTrackingAccurate,
		TimingSource:         s.TimingSource,
		ConnectionReused:     s.ConnectionReused,
		ProbeTransport:       s.ProbeTransport,
		ProbeMethod:          s.ProbeMethod,
		ProbePath:            s.ProbePath,
	}
	if !s.StartedAt.IsZero() {
		out.StartedAt = s.StartedAt.UnixMilli()
	}
	if !s.EndedAt.IsZero() {
		out.EndedAt = s.EndedAt.UnixMilli()
	}
	return out
}

// ThroughputSample represents a single throughput measurement (internal format).
type ThroughputSample struct {
	Timestamp    time.Time     `json:"-"`
	Direction    string        `json:"-"` // "download", "upload"
	SizeBytes    int64         `json:"-"`
	Duration     time.Duration `json:"-"`
	Mbps         float64       `json:"-"`
	Profile      string        `json:"-"`
	RunIndex     int           `json:"-"`
	SampleKind   string        `json:"-"` // "baseline" or "window"
	WindowIndex  int           `json:"-"`
	Concurrency  int           `json:"-"`
	ChunkBytes   int64         `json:"-"`
	RequestCount int           `json:"-"`
	TimingSource string        `json:"-"`
}

// ThroughputSampleJSON is the JSON format matching the web client.
type ThroughputSampleJSON struct {
	Ts           int64   `json:"ts"`
	Direction    string  `json:"direction"`
	SizeBytes    int64   `json:"sizeBytes"`
	DurationMs   float64 `json:"durationMs"`
	Mbps         float64 `json:"mbps"`
	Profile      string  `json:"profile"`
	RunIndex     int     `json:"runIndex"`
	SampleKind   string  `json:"sampleKind,omitempty"`
	WindowIndex  *int    `json:"windowIndex,omitempty"`
	Concurrency  int     `json:"concurrency,omitempty"`
	ChunkBytes   int64   `json:"chunkBytes,omitempty"`
	RequestCount int     `json:"requestCount,omitempty"`
	TimingSource string  `json:"timingSource,omitempty"`
}

// ToJSON converts ThroughputSample to JSON format.
func (s ThroughputSample) ToJSON() ThroughputSampleJSON {
	out := ThroughputSampleJSON{
		Ts:           s.Timestamp.UnixMilli(),
		Direction:    s.Direction,
		SizeBytes:    s.SizeBytes,
		DurationMs:   float64(s.Duration.Microseconds()) / 1000.0,
		Mbps:         s.Mbps,
		Profile:      s.Profile,
		RunIndex:     s.RunIndex,
		SampleKind:   s.SampleKind,
		Concurrency:  s.Concurrency,
		ChunkBytes:   s.ChunkBytes,
		RequestCount: s.RequestCount,
		TimingSource: s.TimingSource,
	}
	if s.SampleKind == "window" {
		windowIndex := s.WindowIndex
		out.WindowIndex = &windowIndex
	}
	return out
}

// RTTStats holds RTT statistics for packet loss test.
type RTTStats struct {
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
}

// PacketLossResult holds the three loss definitions produced by the exact-size
// WebRTC packet test. Sent/Received/LossPercent remain aliases for round-trip
// transaction loss for compatibility with existing result consumers.
type PacketLossResult struct {
	Sent                              int      `json:"sent"`
	Received                          int      `json:"received"`
	LossPercent                       float64  `json:"lossPercent"`
	TransactionLossPercent            float64  `json:"transactionLossPercent"`
	ForwardSent                       int      `json:"forwardSent"`
	ForwardReceived                   int      `json:"forwardReceived"`
	ForwardLossPercent                *float64 `json:"forwardLossPercent"`
	AcknowledgementsSent              int      `json:"acknowledgementsSent"`
	AcknowledgementsReceived          int      `json:"acknowledgementsReceived"`
	ReverseAcknowledgementLossPercent *float64 `json:"reverseAcknowledgementLossPercent"`
	FrameSizeBytes                    int      `json:"frameSizeBytes"`
	DuplicateFrames                   int      `json:"duplicateFrames"`
	InvalidFrames                     int      `json:"invalidFrames"`
	AckSendFailures                   int      `json:"ackSendFailures"`
	RTTStatsMs                        RTTStats `json:"rttStatsMs"`
	JitterMs                          float64  `json:"jitterMs"`
	TestID                            string   `json:"testId,omitempty"`
	Unavailable                       bool     `json:"unavailable,omitempty"`
	Reason                            string   `json:"reason,omitempty"`
}

// MarshalJSON keeps a measured 0% loss distinct from an unavailable test.
func (r PacketLossResult) MarshalJSON() ([]byte, error) {
	type packetLossJSON struct {
		Sent                              int      `json:"sent"`
		Received                          int      `json:"received"`
		LossPercent                       *float64 `json:"lossPercent"`
		TransactionLossPercent            *float64 `json:"transactionLossPercent"`
		ForwardSent                       int      `json:"forwardSent"`
		ForwardReceived                   int      `json:"forwardReceived"`
		ForwardLossPercent                *float64 `json:"forwardLossPercent"`
		AcknowledgementsSent              int      `json:"acknowledgementsSent"`
		AcknowledgementsReceived          int      `json:"acknowledgementsReceived"`
		ReverseAcknowledgementLossPercent *float64 `json:"reverseAcknowledgementLossPercent"`
		FrameSizeBytes                    int      `json:"frameSizeBytes"`
		DuplicateFrames                   int      `json:"duplicateFrames"`
		InvalidFrames                     int      `json:"invalidFrames"`
		AckSendFailures                   int      `json:"ackSendFailures"`
		RTTStatsMs                        RTTStats `json:"rttStatsMs"`
		JitterMs                          float64  `json:"jitterMs"`
		TestID                            string   `json:"testId,omitempty"`
		Unavailable                       bool     `json:"unavailable,omitempty"`
		Reason                            string   `json:"reason,omitempty"`
	}

	var transactionLoss *float64
	forwardLoss := r.ForwardLossPercent
	reverseLoss := r.ReverseAcknowledgementLossPercent
	if !r.Unavailable {
		transactionLoss = &r.TransactionLossPercent
		if r.TransactionLossPercent == 0 && r.LossPercent != 0 {
			transactionLoss = &r.LossPercent
		}
	} else {
		forwardLoss = nil
		reverseLoss = nil
	}
	return json.Marshal(packetLossJSON{
		Sent: r.Sent, Received: r.Received, LossPercent: transactionLoss,
		TransactionLossPercent: transactionLoss,
		ForwardSent:            r.ForwardSent, ForwardReceived: r.ForwardReceived,
		ForwardLossPercent:                forwardLoss,
		AcknowledgementsSent:              r.AcknowledgementsSent,
		AcknowledgementsReceived:          r.AcknowledgementsReceived,
		ReverseAcknowledgementLossPercent: reverseLoss,
		FrameSizeBytes:                    r.FrameSizeBytes, DuplicateFrames: r.DuplicateFrames,
		InvalidFrames: r.InvalidFrames, AckSendFailures: r.AckSendFailures,
		RTTStatsMs: r.RTTStatsMs, JitterMs: r.JitterMs, TestID: r.TestID,
		Unavailable: r.Unavailable, Reason: r.Reason,
	})
}

// Summary holds computed summary statistics.
type Summary struct {
	DownloadMbps      float64  `json:"downloadMbps"`
	UploadMbps        float64  `json:"uploadMbps"`
	LatencyUnloadedMs float64  `json:"latencyUnloadedMs"`
	LatencyDownloadMs float64  `json:"latencyDownloadMs"`
	LatencyUploadMs   float64  `json:"latencyUploadMs"`
	JitterMs          float64  `json:"jitterMs"`
	PacketLossPercent *float64 `json:"packetLossPercent"`
}

// NetworkQuality holds quality grades for different use cases.
// Note: field names match web client exactly ("gaming" not "onlineGaming").
type NetworkQuality struct {
	VideoStreaming string `json:"videoStreaming"`
	Gaming         string `json:"gaming"`
	VideoChatting  string `json:"videoChatting"`
}

// ConfidenceSampleCount records whether the fixed-window and latency sample
// floor required for a high-confidence result was met.
type ConfidenceSampleCount struct {
	DownloadWindows       int  `json:"downloadWindows"`
	UploadWindows         int  `json:"uploadWindows"`
	UnloadedLatency       int  `json:"unloadedLatency"`
	DownloadLoadedLatency int  `json:"downloadLoadedLatency"`
	UploadLoadedLatency   int  `json:"uploadLoadedLatency"`
	Adequate              bool `json:"adequate"`
}

// ConfidenceVariability reports coefficient of variation percentages.
type ConfidenceVariability struct {
	Download   float64 `json:"download"`
	Upload     float64 `json:"upload"`
	Latency    float64 `json:"latency"`
	Acceptable bool    `json:"acceptable"`
}

// ConfidenceOverlap proves loaded probes remained inside continuous load.
type ConfidenceOverlap struct {
	DownloadAccepted int  `json:"downloadAccepted"`
	UploadAccepted   int  `json:"uploadAccepted"`
	Complete         bool `json:"complete"`
}

// ConfidenceTiming identifies whether precise timing sources were available.
type ConfidenceTiming struct {
	Accurate bool `json:"accurate"`
}

// ConfidencePacketTest identifies whether directional packet counters were
// successfully reconciled with the daemon.
type ConfidencePacketTest struct {
	Completed bool `json:"completed"`
}

// TestConfidence is calculated identically by the Go and browser clients.
type TestConfidence struct {
	Overall      string `json:"overall"`
	OverallScore int    `json:"overallScore"`
	Metrics      struct {
		SampleCount   ConfidenceSampleCount `json:"sampleCount"`
		Variability   ConfidenceVariability `json:"coefficientOfVariation"`
		LoadedOverlap ConfidenceOverlap     `json:"loadedOverlap"`
		Timing        ConfidenceTiming      `json:"timingAccuracy"`
		PacketTest    ConfidencePacketTest  `json:"packetTest"`
	} `json:"metrics"`
	Warnings []string `json:"warnings"`
}

// Results holds all test results (internal format).
type Results struct {
	Timestamp         time.Time
	ServerURL         string
	StartTime         time.Time
	EndTime           time.Time
	Meta              *Meta
	ThroughputSamples []ThroughputSample
	LatencySamples    []LatencySample
	PacketLoss        *PacketLossResult
	Summary           Summary
	Quality           NetworkQuality
	TestConfidence    TestConfidence
}

// JSONOutput represents the JSON output format matching the web client exactly.
type JSONOutput struct {
	Meta              *Meta                  `json:"meta"`
	Summary           Summary                `json:"summary"`
	Quality           NetworkQuality         `json:"quality"`
	TestConfidence    TestConfidence         `json:"testConfidence"`
	ThroughputSamples []ThroughputSampleJSON `json:"throughputSamples"`
	LatencySamples    []LatencySampleJSON    `json:"latencySamples"`
	PacketLoss        *PacketLossResult      `json:"packetLoss"`
	StartTime         string                 `json:"startTime"`
	EndTime           string                 `json:"endTime"`
}

// ToJSON converts Results to the JSON output format matching the web client.
func (r *Results) ToJSON() JSONOutput {
	out := JSONOutput{
		Meta:           r.Meta,
		Summary:        r.Summary,
		Quality:        r.Quality,
		TestConfidence: r.TestConfidence,
		PacketLoss:     r.PacketLoss,
		StartTime:      r.StartTime.UTC().Format(time.RFC3339Nano),
		EndTime:        r.EndTime.UTC().Format(time.RFC3339Nano),
	}

	// Convert throughput samples
	out.ThroughputSamples = make([]ThroughputSampleJSON, len(r.ThroughputSamples))
	for i, s := range r.ThroughputSamples {
		out.ThroughputSamples[i] = s.ToJSON()
	}

	// Convert latency samples
	out.LatencySamples = make([]LatencySampleJSON, len(r.LatencySamples))
	for i, s := range r.LatencySamples {
		out.LatencySamples[i] = s.ToJSON()
	}

	return out
}
