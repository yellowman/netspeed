package client

import (
	"time"
)

// Meta holds server and client metadata (matches web client format).
type Meta struct {
	Hostname              string  `json:"hostname"`
	MeasurementAPIVersion int     `json:"measurementApiVersion,omitempty"`
	MaxTransferBytes      int64   `json:"maxTransferBytes,omitempty"`
	ClientIP              string  `json:"clientIp"`
	HTTPProtocol          string  `json:"httpProtocol"`
	ASN                   int     `json:"asn"`
	ASOrganization        string  `json:"asOrganization"`
	Colo                  string  `json:"colo"`
	Country               string  `json:"country"`
	City                  string  `json:"city"`
	Region                string  `json:"region"`
	PostalCode            string  `json:"postalCode"`
	Latitude              float64 `json:"latitude"`
	Longitude             float64 `json:"longitude"`
	Timezone              string  `json:"timezone,omitempty"`
}

// LatencySample represents a single latency measurement (internal format).
type LatencySample struct {
	Timestamp time.Time     `json:"-"`
	RTT       time.Duration `json:"-"`
	Phase     string        `json:"-"` // "unloaded", "download", "upload"
}

// LatencySampleJSON is the JSON format matching the web client.
type LatencySampleJSON struct {
	Ts    int64   `json:"ts"`
	RttMs float64 `json:"rttMs"`
	Phase string  `json:"phase"`
}

// ToJSON converts LatencySample to JSON format.
func (s *LatencySample) ToJSON() LatencySampleJSON {
	return LatencySampleJSON{
		Ts:    s.Timestamp.UnixMilli(),
		RttMs: float64(s.RTT.Microseconds()) / 1000.0,
		Phase: s.Phase,
	}
}

// ThroughputSample represents a single throughput measurement (internal format).
type ThroughputSample struct {
	Timestamp time.Time     `json:"-"`
	Direction string        `json:"-"` // "download", "upload"
	SizeBytes int64         `json:"-"`
	Duration  time.Duration `json:"-"`
	Mbps      float64       `json:"-"`
	Profile   string        `json:"-"`
	RunIndex  int           `json:"-"`
}

// ThroughputSampleJSON is the JSON format matching the web client.
type ThroughputSampleJSON struct {
	Ts         int64   `json:"ts"`
	Direction  string  `json:"direction"`
	SizeBytes  int64   `json:"sizeBytes"`
	DurationMs float64 `json:"durationMs"`
	Mbps       float64 `json:"mbps"`
	Profile    string  `json:"profile"`
	RunIndex   int     `json:"runIndex"`
}

// ToJSON converts ThroughputSample to JSON format.
func (s *ThroughputSample) ToJSON() ThroughputSampleJSON {
	return ThroughputSampleJSON{
		Ts:         s.Timestamp.UnixMilli(),
		Direction:  s.Direction,
		SizeBytes:  s.SizeBytes,
		DurationMs: float64(s.Duration.Microseconds()) / 1000.0,
		Mbps:       s.Mbps,
		Profile:    s.Profile,
		RunIndex:   s.RunIndex,
	}
}

// RTTStats holds RTT statistics for packet loss test.
type RTTStats struct {
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
}

// PacketLossResult holds packet loss test results.
type PacketLossResult struct {
	Sent        int      `json:"sent"`
	Received    int      `json:"received"`
	LossPercent float64  `json:"lossPercent"`
	RTTStatsMs  RTTStats `json:"rttStatsMs"`
	JitterMs    float64  `json:"jitterMs"`
	TestID      string   `json:"testId,omitempty"`
	Unavailable bool     `json:"unavailable,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// Summary holds computed summary statistics.
type Summary struct {
	DownloadMbps      *float64 `json:"downloadMbps"`
	UploadMbps        *float64 `json:"uploadMbps"`
	LatencyUnloadedMs *float64 `json:"latencyUnloadedMs"`
	LatencyDownloadMs *float64 `json:"latencyDownloadMs"`
	LatencyUploadMs   *float64 `json:"latencyUploadMs"`
	JitterMs          *float64 `json:"jitterMs"`
	PacketLossPercent *float64 `json:"packetLossPercent"`
}

// NetworkQuality holds quality grades for different use cases.
// Note: field names match web client exactly ("gaming" not "onlineGaming").
type NetworkQuality struct {
	VideoStreaming string `json:"videoStreaming"`
	Gaming         string `json:"gaming"`
	VideoChatting  string `json:"videoChatting"`
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
}

// JSONOutput represents the JSON output format matching the web client exactly.
type JSONOutput struct {
	Meta              *Meta                  `json:"meta"`
	Summary           Summary                `json:"summary"`
	Quality           NetworkQuality         `json:"quality"`
	ThroughputSamples []ThroughputSampleJSON `json:"throughputSamples"`
	LatencySamples    []LatencySampleJSON    `json:"latencySamples"`
	PacketLoss        *PacketLossResult      `json:"packetLoss"`
	StartTime         string                 `json:"startTime"`
	EndTime           string                 `json:"endTime"`
}

// ToJSON converts Results to the JSON output format matching the web client.
func (r *Results) ToJSON() JSONOutput {
	out := JSONOutput{
		Meta:       r.Meta,
		Summary:    r.Summary,
		Quality:    r.Quality,
		PacketLoss: r.PacketLoss,
		StartTime:  r.StartTime.UTC().Format(time.RFC3339Nano),
		EndTime:    r.EndTime.UTC().Format(time.RFC3339Nano),
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
