// Package output provides terminal output formatting with ASCII spinners and colors.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yellowman/netspeed/cmd/netspeed/client"
)

// ANSI color codes
const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
)

// Config holds output configuration.
type Config struct {
	JSON        bool
	CSV         bool
	Quiet       bool
	Verbose     bool
	Color       bool
	Interactive bool
}

// Output handles formatted terminal output.
type Output struct {
	cfg     Config
	spinner *Spinner
	mu      sync.Mutex
}

// New creates a new Output instance.
func New(cfg Config) *Output {
	return &Output{cfg: cfg}
}

// IsInteractive returns true if running in an interactive terminal.
func IsInteractive() bool {
	if os.Getenv("CI") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// color wraps text in ANSI color codes if colors are enabled.
func (o *Output) color(code, text string) string {
	if !o.cfg.Color {
		return text
	}
	return code + text + ColorReset
}

// Header prints the test header.
func (o *Output) Header(serverURL, version string) {
	if o.cfg.JSON || o.cfg.CSV || o.cfg.Quiet {
		return
	}

	fmt.Printf("%s %s\n", o.color(ColorBold, "netspeed"), displayVersion(version))
	fmt.Printf("Server: %s\n", o.color(ColorCyan, serverURL))
	fmt.Println(strings.Repeat("─", 48))
}

func displayVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "dev"
	}
	if version == "dev" || strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

// Progress updates the progress display.
func (o *Output) Progress(phase string, current, total int, value float64) {
	if o.cfg.JSON || o.cfg.CSV || o.cfg.Quiet {
		return
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if !o.cfg.Interactive {
		// Non-interactive: just print progress
		if o.cfg.Verbose {
			fmt.Printf("%s: %d/%d (%.1f)\n", phase, current, total, value)
		}
		return
	}

	// Interactive: show progress bar
	percent := float64(current) / float64(total)
	barWidth := 20
	filled := int(percent * float64(barWidth))
	empty := barWidth - filled

	bar := strings.Repeat("=", filled)
	if filled < barWidth {
		bar += ">"
		empty--
	}
	bar += strings.Repeat(" ", max(0, empty))

	var valueStr string
	switch phase {
	case "download", "upload":
		valueStr = fmt.Sprintf("%.1f Mbps", value)
	case "latency":
		valueStr = fmt.Sprintf("%.1f ms", value)
	default:
		valueStr = fmt.Sprintf("%.1f", value)
	}

	label := strings.Title(phase)
	fmt.Printf("\r%-12s [%s] %3.0f%% %s\033[K", label+":", bar, percent*100, valueStr)
}

// ClearProgress clears the progress line.
func (o *Output) ClearProgress() {
	if o.cfg.Interactive {
		fmt.Print("\r" + strings.Repeat(" ", 60) + "\r")
	}
}

// Error prints an error message.
func (o *Output) Error(msg string) {
	if o.cfg.JSON {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": msg})
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", o.color(ColorRed, "Error:"), msg)
}

// Results prints the final results.
func (o *Output) Results(r *client.Results) {
	o.ClearProgress()

	fmt.Println()
	fmt.Println(strings.Repeat("─", 48))
	fmt.Printf("%s\n", o.color(ColorBold, "                    RESULTS"))
	fmt.Println(strings.Repeat("─", 48))

	// Download
	dlStr := fmt.Sprintf("%.1f Mbps", r.Summary.DownloadMbps)
	fmt.Printf("  Download:     %s\n", o.color(ColorCyan, dlStr))

	// Upload
	ulStr := fmt.Sprintf("%.1f Mbps", r.Summary.UploadMbps)
	fmt.Printf("  Upload:       %s\n", o.color(ColorCyan, ulStr))

	// Latency
	latStr := fmt.Sprintf("%.1f ms (jitter: %.1f ms)", r.Summary.LatencyUnloadedMs, r.Summary.JitterMs)
	fmt.Printf("  Latency:      %s\n", o.color(ColorBlue, latStr))

	// Packet loss. The compatibility summary is round-trip transaction loss;
	// Phase 2 also exposes forward probes and reverse acknowledgements.
	if r.PacketLoss != nil && r.PacketLoss.Unavailable {
		fmt.Printf("  Packet Loss:  %s\n", o.color(ColorYellow, "N/A ("+r.PacketLoss.Reason+")"))
	} else if r.PacketLoss != nil {
		plStr := fmt.Sprintf("%.2f%% transaction (%d/%d)",
			r.PacketLoss.TransactionLossPercent, r.PacketLoss.Received, r.PacketLoss.Sent)
		fmt.Printf("  Packet Loss:  %s\n", plStr)
		fmt.Printf("    Forward:    %s\n", formatDirectionalLoss(
			r.PacketLoss.ForwardLossPercent,
			r.PacketLoss.ForwardReceived,
			r.PacketLoss.ForwardSent,
		))
		fmt.Printf("    Reverse ACK:%s\n", formatDirectionalLoss(
			r.PacketLoss.ReverseAcknowledgementLossPercent,
			r.PacketLoss.AcknowledgementsReceived,
			r.PacketLoss.AcknowledgementsSent,
		))
	} else {
		fmt.Printf("  Packet Loss:  %s\n", o.color(ColorYellow, "N/A (skipped)"))
	}

	confidence := fmt.Sprintf("%s (%d/100)", strings.ToUpper(r.TestConfidence.Overall), r.TestConfidence.OverallScore)
	fmt.Printf("  Confidence:   %s\n", o.confidenceColor(r.TestConfidence.Overall, confidence))

	fmt.Println(strings.Repeat("─", 48))

	// Network Quality
	fmt.Printf("  %s\n", o.color(ColorBold, "Network Quality:"))
	fmt.Printf("    Video Streaming:  %s\n", o.gradeColor(r.Quality.VideoStreaming))
	fmt.Printf("    Online Gaming:    %s\n", o.gradeColor(r.Quality.Gaming))
	fmt.Printf("    Video Chatting:   %s\n", o.gradeColor(r.Quality.VideoChatting))

	fmt.Println(strings.Repeat("─", 48))

	// Verbose details
	if o.cfg.Verbose {
		o.verboseDetails(r)
	}
}

func (o *Output) gradeColor(grade string) string {
	switch grade {
	case "Great":
		return o.color(ColorGreen, grade)
	case "Good":
		return o.color(ColorGreen, grade)
	case "Okay", "Incomplete":
		return o.color(ColorYellow, grade)
	case "Poor":
		return o.color(ColorRed, grade)
	default:
		return grade
	}
}

func (o *Output) confidenceColor(level, text string) string {
	switch strings.ToLower(level) {
	case "high":
		return o.color(ColorGreen, text)
	case "medium":
		return o.color(ColorYellow, text)
	case "low":
		return o.color(ColorRed, text)
	default:
		return text
	}
}

func formatDirectionalLoss(loss *float64, received, sent int) string {
	if loss == nil || sent <= 0 {
		return " N/A"
	}
	return fmt.Sprintf(" %.2f%% (%d/%d)", *loss, received, sent)
}

func (o *Output) verboseDetails(r *client.Results) {
	fmt.Println()
	fmt.Println(o.color(ColorBold, "LATENCY BREAKDOWN"))
	fmt.Println(strings.Repeat("─", 48))

	// Group latency samples by phase
	unloaded := make([]float64, 0)
	for _, s := range r.LatencySamples {
		if s.Phase == "unloaded" {
			unloaded = append(unloaded, float64(s.RTT.Microseconds())/1000)
		}
	}

	if len(unloaded) > 0 {
		min, max, _ := stats(unloaded)
		med := median(unloaded)
		fmt.Printf("  Unloaded:   %.1f ms (min: %.1f, max: %.1f, median: %.1f)\n",
			r.Summary.LatencyUnloadedMs, min, max, med)
	}

	fmt.Println()
	fmt.Println(o.color(ColorBold, "DOWNLOAD TESTS"))
	fmt.Println(strings.Repeat("─", 48))

	// Group by profile
	dlByProfile := make(map[string][]float64)
	for _, s := range r.ThroughputSamples {
		if s.Direction == "download" {
			dlByProfile[s.Profile] = append(dlByProfile[s.Profile], s.Mbps)
		}
	}

	for profile, speeds := range dlByProfile {
		min, max, avg := stats(speeds)
		fmt.Printf("  %-8s x%d:  avg %.1f Mbps  (min: %.0f, max: %.0f)\n",
			profile, len(speeds), avg, min, max)
	}

	fmt.Println()
	fmt.Println(o.color(ColorBold, "UPLOAD TESTS"))
	fmt.Println(strings.Repeat("─", 48))

	ulByProfile := make(map[string][]float64)
	for _, s := range r.ThroughputSamples {
		if s.Direction == "upload" {
			ulByProfile[s.Profile] = append(ulByProfile[s.Profile], s.Mbps)
		}
	}

	for profile, speeds := range ulByProfile {
		min, max, avg := stats(speeds)
		fmt.Printf("  %-8s x%d:  avg %.1f Mbps  (min: %.0f, max: %.0f)\n",
			profile, len(speeds), avg, min, max)
	}

	if r.PacketLoss != nil && !r.PacketLoss.Unavailable {
		fmt.Println()
		fmt.Println(o.color(ColorBold, "PACKET LOSS TEST"))
		fmt.Println(strings.Repeat("─", 48))
		fmt.Printf("  Frame:      %d bytes, exact binary v1\n", r.PacketLoss.FrameSizeBytes)
		fmt.Printf("  Transaction:%s\n", formatDirectionalLoss(
			&r.PacketLoss.TransactionLossPercent,
			r.PacketLoss.Received,
			r.PacketLoss.Sent,
		))
		fmt.Printf("  Forward:    %s\n", formatDirectionalLoss(
			r.PacketLoss.ForwardLossPercent,
			r.PacketLoss.ForwardReceived,
			r.PacketLoss.ForwardSent,
		))
		fmt.Printf("  Reverse ACK:%s\n", formatDirectionalLoss(
			r.PacketLoss.ReverseAcknowledgementLossPercent,
			r.PacketLoss.AcknowledgementsReceived,
			r.PacketLoss.AcknowledgementsSent,
		))
		fmt.Printf("  Server:     duplicates %d, invalid %d, ACK send failures %d\n",
			r.PacketLoss.DuplicateFrames, r.PacketLoss.InvalidFrames, r.PacketLoss.AckSendFailures)
		fmt.Printf("  RTT:        min %.1f ms, median %.1f ms, p90 %.1f ms\n",
			r.PacketLoss.RTTStatsMs.Min, r.PacketLoss.RTTStatsMs.Median, r.PacketLoss.RTTStatsMs.P90)
		fmt.Printf("  Jitter:     %.1f ms\n", r.PacketLoss.JitterMs)
	}

	fmt.Println()
	fmt.Println(o.color(ColorBold, "MEASUREMENT CONFIDENCE"))
	fmt.Println(strings.Repeat("─", 48))
	fmt.Printf("  Overall:    %s (%d/100)\n", strings.ToUpper(r.TestConfidence.Overall), r.TestConfidence.OverallScore)
	fmt.Printf("  Samples:    windows d=%d u=%d; latency unloaded=%d loaded d=%d u=%d\n",
		r.TestConfidence.Metrics.SampleCount.DownloadWindows,
		r.TestConfidence.Metrics.SampleCount.UploadWindows,
		r.TestConfidence.Metrics.SampleCount.UnloadedLatency,
		r.TestConfidence.Metrics.SampleCount.DownloadLoadedLatency,
		r.TestConfidence.Metrics.SampleCount.UploadLoadedLatency)
	fmt.Printf("  Variability: download %.1f%%, upload %.1f%%, latency %.1f%%\n",
		r.TestConfidence.Metrics.Variability.Download,
		r.TestConfidence.Metrics.Variability.Upload,
		r.TestConfidence.Metrics.Variability.Latency)
	for _, warning := range r.TestConfidence.Warnings {
		fmt.Printf("  Warning:    %s\n", warning)
	}
}

func stats(values []float64) (min, max, avg float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}

	min = values[0]
	max = values[0]
	sum := 0.0

	for _, v := range values {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}

	avg = sum / float64(len(values))
	return
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

func optionalFloat(value *float64, format string) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf(format, *value)
}

// Quiet prints minimal output.
func (o *Output) Quiet(r *client.Results) {
	// Format: download_mbps upload_mbps latency_ms loss_percent
	fmt.Printf("%.1f  %.1f  %.1f  %s\n",
		r.Summary.DownloadMbps,
		r.Summary.UploadMbps,
		r.Summary.LatencyUnloadedMs,
		optionalFloat(r.Summary.PacketLossPercent, "%.2f"))
}

// CSV prints CSV output.
func (o *Output) CSV(r *client.Results) {
	fmt.Println("timestamp,server,download_mbps,upload_mbps,latency_ms,jitter_ms,packet_loss_pct")

	hostname := ""
	if r.Meta != nil {
		hostname = r.Meta.Hostname
	}
	loss := ""
	if r.Summary.PacketLossPercent != nil {
		loss = fmt.Sprintf("%.2f", *r.Summary.PacketLossPercent)
	}
	fmt.Printf("%s,%s,%.1f,%.1f,%.1f,%.1f,%s\n",
		r.Timestamp.UTC().Format(time.RFC3339),
		hostname,
		r.Summary.DownloadMbps,
		r.Summary.UploadMbps,
		r.Summary.LatencyUnloadedMs,
		r.Summary.JitterMs,
		loss)
}

// Spinner provides ASCII spinner animation.
type Spinner struct {
	frames   []string
	current  int
	prefix   string
	stop     chan struct{}
	stopped  bool
	mu       sync.Mutex
	interval time.Duration
}

// NewSpinner creates a new spinner.
func NewSpinner() *Spinner {
	return &Spinner{
		frames:   []string{"|", "/", "-", "\\"},
		interval: 100 * time.Millisecond,
		stop:     make(chan struct{}),
	}
}

// Start starts the spinner with the given prefix.
func (s *Spinner) Start(prefix string) {
	s.mu.Lock()
	s.prefix = prefix
	s.stopped = false
	s.mu.Unlock()

	go func() {
		for {
			select {
			case <-s.stop:
				return
			default:
				s.mu.Lock()
				if !s.stopped {
					fmt.Printf("\r%s %s", s.prefix, s.frames[s.current])
					s.current = (s.current + 1) % len(s.frames)
				}
				s.mu.Unlock()
				time.Sleep(s.interval)
			}
		}
	}()
}

// Stop stops the spinner and prints the final text.
func (s *Spinner) Stop(finalText string) {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()

	select {
	case s.stop <- struct{}{}:
	default:
	}

	fmt.Printf("\r%s\n", finalText)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
