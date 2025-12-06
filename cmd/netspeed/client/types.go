package client

import (
	"time"
)

// Meta holds server and client metadata.
type Meta struct {
	Hostname       string  `json:"hostname"`
	ClientIP       string  `json:"clientIp"`
	HTTPProtocol   string  `json:"httpProtocol"`
	ASN            int     `json:"asn"`
	ASOrganization string  `json:"asOrganization"`
	Colo           string  `json:"colo"`
	Country        string  `json:"country"`
	City           string  `json:"city"`
	Region         string  `json:"region"`
	PostalCode     string  `json:"postalCode"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	Timezone       string  `json:"timezone"`
}

// LatencySample represents a single latency measurement.
type LatencySample struct {
	Timestamp time.Time     `json:"timestamp"`
	RTT       time.Duration `json:"rtt"`
	Phase     string        `json:"phase"` // "unloaded", "download", "upload"
}

// ThroughputSample represents a single throughput measurement.
type ThroughputSample struct {
	Timestamp  time.Time     `json:"timestamp"`
	Direction  string        `json:"direction"` // "download", "upload"
	SizeBytes  int64         `json:"sizeBytes"`
	Duration   time.Duration `json:"duration"`
	Mbps       float64       `json:"mbps"`
	Profile    string        `json:"profile"`
	RunIndex   int           `json:"runIndex"`
}

// PacketLossResult holds packet loss test results.
type PacketLossResult struct {
	Sent        int     `json:"sent"`
	Received    int     `json:"received"`
	LossPercent float64 `json:"lossPercent"`
	RTTStats    struct {
		MinMs    float64 `json:"min"`
		MedianMs float64 `json:"median"`
		P90Ms    float64 `json:"p90"`
	} `json:"rttStatsMs"`
	JitterMs    float64 `json:"jitterMs"`
	TestID      string  `json:"testId,omitempty"`
	Unavailable bool    `json:"unavailable,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

// Summary holds computed summary statistics.
type Summary struct {
	DownloadMbps      float64 `json:"downloadMbps"`
	UploadMbps        float64 `json:"uploadMbps"`
	LatencyUnloadedMs float64 `json:"latencyUnloadedMs"`
	LatencyDownloadMs float64 `json:"latencyDownloadMs"`
	LatencyUploadMs   float64 `json:"latencyUploadMs"`
	JitterMs          float64 `json:"jitterMs"`
	PacketLossPercent float64 `json:"packetLossPercent"`
}

// NetworkQuality holds quality grades for different use cases.
type NetworkQuality struct {
	VideoStreaming string `json:"videoStreaming"`
	OnlineGaming   string `json:"onlineGaming"`
	VideoChatting  string `json:"videoChatting"`
}

// Results holds all test results.
type Results struct {
	Timestamp         time.Time          `json:"timestamp"`
	ServerURL         string             `json:"serverUrl"`
	Meta              *Meta              `json:"meta"`
	ThroughputSamples []ThroughputSample `json:"throughputSamples"`
	LatencySamples    []LatencySample    `json:"latencySamples"`
	PacketLoss        *PacketLossResult  `json:"packetLoss"`
	Summary           Summary            `json:"summary"`
	Quality           NetworkQuality     `json:"quality"`
}

// JSONOutput represents the JSON output format matching the web client.
type JSONOutput struct {
	Timestamp string `json:"timestamp"`
	Server    struct {
		Hostname string `json:"hostname"`
		Colo     string `json:"colo"`
		City     string `json:"city"`
		Country  string `json:"country"`
	} `json:"server"`
	Client struct {
		IP           string `json:"ip"`
		ASN          int    `json:"asn"`
		Organization string `json:"organization"`
		City         string `json:"city"`
		Region       string `json:"region"`
		Country      string `json:"country"`
	} `json:"client"`
	Results struct {
		Download struct {
			Mbps    float64 `json:"mbps"`
			Samples int     `json:"samples"`
		} `json:"download"`
		Upload struct {
			Mbps    float64 `json:"mbps"`
			Samples int     `json:"samples"`
		} `json:"upload"`
		Latency struct {
			UnloadedMs float64 `json:"unloaded_ms"`
			DownloadMs float64 `json:"download_ms"`
			UploadMs   float64 `json:"upload_ms"`
			JitterMs   float64 `json:"jitter_ms"`
		} `json:"latency"`
		PacketLoss struct {
			Percent     float64 `json:"percent"`
			Sent        int     `json:"sent"`
			Received    int     `json:"received"`
			RTTMinMs    float64 `json:"rtt_min_ms"`
			RTTMedianMs float64 `json:"rtt_median_ms"`
			RTTP90Ms    float64 `json:"rtt_p90_ms"`
			Unavailable bool    `json:"unavailable,omitempty"`
			Reason      string  `json:"reason,omitempty"`
		} `json:"packet_loss"`
	} `json:"results"`
	Quality struct {
		VideoStreaming string `json:"video_streaming"`
		OnlineGaming   string `json:"online_gaming"`
		VideoChatting  string `json:"video_chatting"`
	} `json:"quality"`
}

// ToJSON converts Results to the JSON output format.
func (r *Results) ToJSON() JSONOutput {
	var out JSONOutput

	out.Timestamp = r.Timestamp.UTC().Format(time.RFC3339)

	if r.Meta != nil {
		out.Server.Hostname = r.Meta.Hostname
		out.Server.Colo = r.Meta.Colo
		out.Server.City = r.Meta.City
		out.Server.Country = r.Meta.Country

		out.Client.IP = r.Meta.ClientIP
		out.Client.ASN = r.Meta.ASN
		out.Client.Organization = r.Meta.ASOrganization
		out.Client.City = r.Meta.City
		out.Client.Region = r.Meta.Region
		out.Client.Country = r.Meta.Country
	}

	// Count samples
	dlCount := 0
	ulCount := 0
	for _, s := range r.ThroughputSamples {
		if s.Direction == "download" {
			dlCount++
		} else {
			ulCount++
		}
	}

	out.Results.Download.Mbps = r.Summary.DownloadMbps
	out.Results.Download.Samples = dlCount
	out.Results.Upload.Mbps = r.Summary.UploadMbps
	out.Results.Upload.Samples = ulCount
	out.Results.Latency.UnloadedMs = r.Summary.LatencyUnloadedMs
	out.Results.Latency.DownloadMs = r.Summary.LatencyDownloadMs
	out.Results.Latency.UploadMs = r.Summary.LatencyUploadMs
	out.Results.Latency.JitterMs = r.Summary.JitterMs

	if r.PacketLoss != nil {
		out.Results.PacketLoss.Percent = r.PacketLoss.LossPercent
		out.Results.PacketLoss.Sent = r.PacketLoss.Sent
		out.Results.PacketLoss.Received = r.PacketLoss.Received
		out.Results.PacketLoss.RTTMinMs = r.PacketLoss.RTTStats.MinMs
		out.Results.PacketLoss.RTTMedianMs = r.PacketLoss.RTTStats.MedianMs
		out.Results.PacketLoss.RTTP90Ms = r.PacketLoss.RTTStats.P90Ms
		out.Results.PacketLoss.Unavailable = r.PacketLoss.Unavailable
		out.Results.PacketLoss.Reason = r.PacketLoss.Reason
	}

	out.Quality.VideoStreaming = r.Quality.VideoStreaming
	out.Quality.OnlineGaming = r.Quality.OnlineGaming
	out.Quality.VideoChatting = r.Quality.VideoChatting

	return out
}
