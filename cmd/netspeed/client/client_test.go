package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/protocol"
)

func testClient(server *httptest.Server) *Client {
	c := New(Config{ServerURL: server.URL})
	c.httpClient = server.Client()
	c.maxTransferBytes = 2_000_000
	c.uploadReceiptVersion = protocol.UploadReceiptVersion
	return c
}

func TestContainsTURNURL(t *testing.T) {
	if containsTURNURL([]string{"stun:stun.example.test:3478", "stuns:stun.example.test:5349"}) {
		t.Fatal("STUN-only list was classified as TURN")
	}
	if !containsTURNURL([]string{"stun:stun.example.test:3478", " TuRn:relay.example.test:3478?transport=udp "}) {
		t.Fatal("TURN URL was not detected")
	}
}

func TestSelectWindowPlanBoundsMemoryAndScalesWithConcurrency(t *testing.T) {
	plan := selectWindowPlan(1_000_000, 1<<30, false)
	if plan.Concurrency != 16 {
		t.Fatalf("concurrency = %d; want 16 at extreme estimated rate", plan.Concurrency)
	}
	if plan.ChunkBytes != maxWindowChunkBytes {
		t.Fatalf("chunk = %d; want bounded maximum %d", plan.ChunkBytes, maxWindowChunkBytes)
	}
	if plan.Windows != 3 || plan.LoadedWindow != 1 || plan.LoadedProbeCount != 5 {
		t.Fatalf("full plan = %#v; want three windows with loaded probes in the middle", plan)
	}

	limited := selectWindowPlan(1_000_000, 750_000, true)
	if limited.ChunkBytes != 750_000 {
		t.Fatalf("limited chunk = %d; want server cap 750000", limited.ChunkBytes)
	}
	if limited.Windows != 1 || limited.LoadedWindow != 0 || limited.LoadedProbeCount != 3 || limited.WindowDuration != quickWindowDuration {
		t.Fatalf("quick plan = %#v", limited)
	}
}

func TestRequiredSuccessfulRuns(t *testing.T) {
	for total, want := range map[int]int{0: 0, 1: 1, 2: 1, 3: 2, 4: 2, 5: 3} {
		if got := requiredSuccessfulRuns(total); got != want {
			t.Fatalf("requiredSuccessfulRuns(%d) = %d; want %d", total, got, want)
		}
	}
}

func TestRunThroughputWindowUsesBoundedRepeatedRequests(t *testing.T) {
	const chunkBytes = int64(64 * 1024)
	var active int64
	var maxActive int64
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if err != nil {
			t.Errorf("parse requested bytes: %v", err)
			return
		}
		if requested != chunkBytes {
			t.Errorf("requested %d bytes; want bounded chunk %d", requested, chunkBytes)
		}
		current := atomic.AddInt64(&active, 1)
		for {
			observed := atomic.LoadInt64(&maxActive)
			if current <= observed || atomic.CompareAndSwapInt64(&maxActive, observed, current) {
				break
			}
		}
		// Keep the request in flight long enough to observe true worker
		// concurrency, then stop counting before writing the final response.
		// A client can observe EOF just before a handler's deferred cleanup runs,
		// which would otherwise make sequential requests look concurrent.
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt64(&active, -1)
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(chunkBytes, 10))
		_, _ = io.CopyN(w, zeroReader{}, chunkBytes)
	}))
	defer server.Close()

	c := testClient(server)
	c.maxTransferBytes = 1_000_000
	plan := windowPlan{
		ChunkBytes: chunkBytes, Concurrency: 3, WindowDuration: 80 * time.Millisecond,
		Windows: 1, LoadedWindow: -1,
	}
	sample, probes, err := c.runThroughputWindow(context.Background(), "download", plan, 0, false)
	if err != nil {
		t.Fatalf("runThroughputWindow: %v", err)
	}
	if len(probes) != 0 {
		t.Fatalf("probes = %d; want none", len(probes))
	}
	if sample.SampleKind != "window" || sample.ChunkBytes != chunkBytes || sample.Concurrency != 3 {
		t.Fatalf("window sample = %#v", sample)
	}
	if sample.RequestCount < 3 || sample.SizeBytes != int64(sample.RequestCount)*chunkBytes {
		t.Fatalf("request accounting = %#v", sample)
	}
	if got := atomic.LoadInt64(&maxActive); got > 3 {
		t.Fatalf("server observed %d concurrent requests; plan allowed 3", got)
	}
	if got := atomic.LoadInt64(&requests); got != int64(sample.RequestCount) {
		t.Fatalf("server requests = %d; sample requestCount = %d", got, sample.RequestCount)
	}
}

func TestRunLoadedLatencyProbesRejectsProbeAcrossLoadGap(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requestCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("bytes") != "0" {
			t.Errorf("latency bytes = %q; want 0", r.URL.Query().Get("bytes"))
		}
		request := atomic.AddInt64(&requestCount, 1)
		if request == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := testClient(server)
	activity := &loadActivity{}
	activity.begin()
	defer activity.end()

	type probeResult struct {
		samples []LatencySample
		err     error
	}
	finished := make(chan probeResult, 1)
	go func() {
		samples, err := c.runLoadedLatencyProbes(context.Background(), "download", 1, activity)
		finished <- probeResult{samples: samples, err: err}
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first latency probe did not start")
	}
	activity.end()
	activity.begin()
	close(releaseFirst)

	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runLoadedLatencyProbes: %v", result.err)
		}
		if len(result.samples) != 1 || !result.samples[0].LoadOverlapped {
			t.Fatalf("samples = %#v; want one proven-overlap retry", result.samples)
		}
		if got := atomic.LoadInt64(&requestCount); got < 2 {
			t.Fatalf("request count = %d; first gap-crossing probe was not rejected", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loaded latency probe test timed out")
	}
}

func TestRunLoadedLatencyProbesAcceptsQuorumAtWindowDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("bytes") != "0" {
			t.Errorf("latency bytes = %q; want 0", r.URL.Query().Get("bytes"))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := testClient(server)
	c.cfg.OnProgress = func(phase string, current, total int, value float64) {
		if phase == "loaded-latency" && current == requiredSuccessfulRuns(total) {
			cancel()
		}
	}
	activity := &loadActivity{}
	activity.begin()
	defer activity.end()

	started := time.Now()
	samples, err := c.runLoadedLatencyProbes(ctx, "download", 5, activity)
	if err != nil {
		t.Fatalf("runLoadedLatencyProbes: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("samples = %d; want quorum 3 after deadline cancellation", len(samples))
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline-aware probe loop took %s; want prompt quorum return", elapsed)
	}
}

func TestMeasureDownloadValidatesAndCountsExactBody(t *testing.T) {
	const size = int64(64 * 1024)
	payload := bytes.Repeat([]byte{0x5a}, int(size))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("bytes"); got != "65536" {
			t.Errorf("bytes query = %q; want 65536", got)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "65536")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload[:1])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(2 * time.Millisecond)
		_, _ = w.Write(payload[1:])
	}))
	defer server.Close()

	sample, err := testClient(server).measureDownload(context.Background(), "test", size, 0)
	if err != nil {
		t.Fatalf("measureDownload returned error: %v", err)
	}
	if sample.SizeBytes != size {
		t.Fatalf("sample.SizeBytes = %d; want %d", sample.SizeBytes, size)
	}
	if sample.Duration <= 0 || sample.Mbps <= 0 {
		t.Fatalf("invalid sample timing/speed: duration=%s mbps=%f", sample.Duration, sample.Mbps)
	}
}

func TestMeasureDownloadRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "broken", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := testClient(server).measureDownload(context.Background(), "test", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("measureDownload error = %v; want HTTP 500", err)
	}
}

func TestMeasureDownloadRejectsShortBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("1234"))
	}))
	defer server.Close()

	_, err := testClient(server).measureDownload(context.Background(), "test", 8, 0)
	if err == nil {
		t.Fatal("measureDownload accepted a truncated response body")
	}
}

func TestMeasureDownloadRejectsExtraChunkedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("1234"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("5"))
	}))
	defer server.Close()

	_, err := testClient(server).measureDownload(context.Background(), "test", 4, 0)
	if err == nil || !strings.Contains(err.Error(), "received 5 bytes; expected 4") {
		t.Fatalf("measureDownload error = %v; want extra-body rejection", err)
	}
}

func TestMeasureDownloadRejectsWrongContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	_, err := testClient(server).measureDownload(context.Background(), "test", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("measureDownload error = %v; want content-type rejection", err)
	}
}

func TestMeasureUploadRequiresMatchingReceipt(t *testing.T) {
	const size = int64(128 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Errorf("read upload: %v", err)
		}
		if n != size {
			t.Errorf("received %d bytes; want %d", n, size)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.UploadReceipt{
			OK:               true,
			AcceptedBytes:    n,
			ServerDurationNS: int64(5 * time.Millisecond),
		})
	}))
	defer server.Close()

	sample, err := testClient(server).measureUpload(context.Background(), "test", size, 0)
	if err != nil {
		t.Fatalf("measureUpload returned error: %v", err)
	}
	if sample.SizeBytes != size || sample.Duration != 5*time.Millisecond || sample.Mbps <= 0 {
		t.Fatalf("unexpected sample: %#v", sample)
	}
}

func TestMeasureUploadRejectsMismatchedReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.UploadReceipt{
			OK:               true,
			AcceptedBytes:    3,
			ServerDurationNS: int64(time.Millisecond),
		})
	}))
	defer server.Close()

	_, err := testClient(server).measureUpload(context.Background(), "test", 4, 0)
	if err == nil || !strings.Contains(err.Error(), "accepted 3") {
		t.Fatalf("measureUpload error = %v; want receipt mismatch", err)
	}
}

func TestMeasureUploadRejectsTrailingReceiptData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"acceptedBytes":4,"serverDurationNs":1000000} {"extra":true}`)
	}))
	defer server.Close()

	_, err := testClient(server).measureUpload(context.Background(), "test", 4, 0)
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("measureUpload error = %v; want trailing JSON rejection", err)
	}
}

func TestMeasureUploadRejectsOversizedReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat(" ", maxUploadReceiptBodyBytes+1))
	}))
	defer server.Close()

	_, err := testClient(server).measureUpload(context.Background(), "test", 4, 0)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("measureUpload error = %v; want oversized receipt rejection", err)
	}
}

func TestMeasureUploadRejectsMissingReceiptCapability(t *testing.T) {
	c := New(Config{ServerURL: "http://example.invalid"})
	c.uploadReceiptVersion = 0
	_, err := c.measureUpload(context.Background(), "test", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "verified upload receipts") {
		t.Fatalf("measureUpload error = %v; want capability rejection", err)
	}
}

func TestTimedRequestBodyStreamsExactZeroFilledLength(t *testing.T) {
	body := newTimedRequestBody(4097)
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if len(got) != 4097 {
		t.Fatalf("streamed %d bytes; want 4097", len(got))
	}
	for i, b := range got {
		if b != 0 {
			t.Fatalf("byte %d = %d; want zero", i, b)
		}
	}
	if body.bytesRead != 4097 {
		t.Fatalf("body.bytesRead = %d; want 4097", body.bytesRead)
	}
}

func TestConsumeExactBodyIsBounded(t *testing.T) {
	n, err := consumeExactBody(strings.NewReader("12345"), 4)
	if err == nil || !strings.Contains(err.Error(), "received 5 bytes; expected 4") {
		t.Fatalf("consumeExactBody error = %v; want exact-length rejection", err)
	}
	if n != 5 {
		t.Fatalf("consumeExactBody read %d bytes; want bounded 5-byte detection read", n)
	}
}

func TestDecodeLimitedJSONRejectsTrailingAndOversizedBodies(t *testing.T) {
	var value map[string]bool
	if err := decodeLimitedJSON(strings.NewReader(`{"ok":true}`), 64, &value); err != nil {
		t.Fatalf("decodeLimitedJSON valid body: %v", err)
	}
	if !value["ok"] {
		t.Fatalf("decoded value = %#v; want ok=true", value)
	}

	if err := decodeLimitedJSON(strings.NewReader(`{"ok":true}{"extra":true}`), 64, &value); err == nil {
		t.Fatal("decodeLimitedJSON accepted a second JSON value")
	}
	if err := decodeLimitedJSON(strings.NewReader(strings.Repeat(" ", 65)), 64, &value); err == nil {
		t.Fatal("decodeLimitedJSON accepted an oversized body")
	}
}

func TestMeasurementURLQuotesProfileAndPhase(t *testing.T) {
	got := buildMeasurementURL("http://example.test/", "/__down", map[string][]string{
		"profile": {"a profile&more"},
		"during":  {"download/load"},
	})
	if !strings.Contains(got, "profile=a+profile%26more") || !strings.Contains(got, "during=download%2Fload") {
		t.Fatalf("buildMeasurementURL returned %q; want escaped query parameters", got)
	}
}

func TestCalculateSummaryPreservesUnavailablePacketLoss(t *testing.T) {
	c := New(Config{})

	summary := c.calculateSummary(&Results{})
	if summary.PacketLossPercent != nil {
		t.Fatalf("packet loss = %v; want nil", *summary.PacketLossPercent)
	}
	quality := c.calculateQuality(summary)
	if quality.VideoStreaming != "Incomplete" || quality.Gaming != "Incomplete" || quality.VideoChatting != "Incomplete" {
		t.Fatalf("quality = %#v; want all Incomplete", quality)
	}

	summary = c.calculateSummary(&Results{PacketLoss: &PacketLossResult{Unavailable: true, LossPercent: 0}})
	if summary.PacketLossPercent != nil {
		t.Fatalf("unavailable packet loss became %v; want nil", *summary.PacketLossPercent)
	}

	summary = c.calculateSummary(&Results{PacketLoss: &PacketLossResult{LossPercent: 1.25}})
	if summary.PacketLossPercent == nil || math.Abs(*summary.PacketLossPercent-1.25) > 1e-9 {
		t.Fatalf("valid packet loss = %v; want 1.25", summary.PacketLossPercent)
	}
}

func TestPacketLossJSONDistinguishesUnavailableFromMeasuredZero(t *testing.T) {
	unavailable, err := json.Marshal(PacketLossResult{Unavailable: true, Reason: "no channel"})
	if err != nil {
		t.Fatalf("marshal unavailable packet loss: %v", err)
	}
	if !strings.Contains(string(unavailable), `"lossPercent":null`) {
		t.Fatalf("unavailable JSON = %s; want null lossPercent", unavailable)
	}

	measured, err := json.Marshal(PacketLossResult{LossPercent: 0, Sent: 1000, Received: 1000})
	if err != nil {
		t.Fatalf("marshal measured packet loss: %v", err)
	}
	if !strings.Contains(string(measured), `"lossPercent":0`) {
		t.Fatalf("measured JSON = %s; want numeric zero lossPercent", measured)
	}
}

func TestRunRejectsConflictingDirectionModes(t *testing.T) {
	c := New(Config{DownloadOnly: true, UploadOnly: true})
	_, err := c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Run error = %v; want mutually exclusive modes", err)
	}
}

func TestCalculateSummaryUsesWindowSamplesAndSharedStatistics(t *testing.T) {
	c := New(Config{})
	packetLoss := 1.25
	results := &Results{
		ThroughputSamples: []ThroughputSample{
			{Direction: "download", Mbps: 5_000, Duration: 100 * time.Millisecond, SampleKind: "baseline", Profile: "1MB"},
			{Direction: "download", Mbps: 100, Duration: time.Second, SampleKind: "window", Profile: "window"},
			{Direction: "download", Mbps: 110, Duration: time.Second, SampleKind: "window", Profile: "window"},
			{Direction: "download", Mbps: 120, Duration: time.Second, SampleKind: "window", Profile: "window"},
			{Direction: "upload", Mbps: 50, Duration: time.Second, SampleKind: "window", Profile: "window"},
			{Direction: "upload", Mbps: 60, Duration: time.Second, SampleKind: "window", Profile: "window"},
			{Direction: "upload", Mbps: 70, Duration: time.Second, SampleKind: "window", Profile: "window"},
		},
		LatencySamples: []LatencySample{
			{Phase: "unloaded", RTT: 100 * time.Millisecond},
			{Phase: "unloaded", RTT: 200 * time.Millisecond},
			{Phase: "unloaded", RTT: 10 * time.Millisecond},
			{Phase: "unloaded", RTT: 20 * time.Millisecond},
			{Phase: "unloaded", RTT: 30 * time.Millisecond},
			{Phase: "download", RTT: 10 * time.Millisecond, LoadOverlapped: true},
			{Phase: "download", RTT: 20 * time.Millisecond, LoadOverlapped: true},
			{Phase: "download", RTT: 30 * time.Millisecond, LoadOverlapped: true},
			{Phase: "download", RTT: 10 * time.Second, LoadOverlapped: false},
			{Phase: "upload", RTT: 20 * time.Millisecond, LoadOverlapped: true},
			{Phase: "upload", RTT: 30 * time.Millisecond, LoadOverlapped: true},
			{Phase: "upload", RTT: 40 * time.Millisecond, LoadOverlapped: true},
		},
		PacketLoss: &PacketLossResult{
			LossPercent:            packetLoss,
			TransactionLossPercent: packetLoss,
		},
	}

	summary := c.calculateSummary(results)
	if math.Abs(summary.DownloadMbps-118) > 1e-9 {
		t.Fatalf("download p90 = %v; want R-7 p90 118 from windows only", summary.DownloadMbps)
	}
	if math.Abs(summary.UploadMbps-68) > 1e-9 {
		t.Fatalf("upload p90 = %v; want 68", summary.UploadMbps)
	}
	if math.Abs(summary.LatencyUnloadedMs-20) > 1e-9 || math.Abs(summary.JitterMs-8) > 1e-9 {
		t.Fatalf("unloaded latency/jitter = %.3f/%.3f; want 20/8", summary.LatencyUnloadedMs, summary.JitterMs)
	}
	if math.Abs(summary.LatencyDownloadMs-28) > 1e-9 {
		t.Fatalf("download loaded p90 = %v; want 28 with non-overlap sample excluded", summary.LatencyDownloadMs)
	}
	if math.Abs(summary.LatencyUploadMs-38) > 1e-9 {
		t.Fatalf("upload loaded p90 = %v; want 38", summary.LatencyUploadMs)
	}
	if summary.PacketLossPercent == nil || math.Abs(*summary.PacketLossPercent-packetLoss) > 1e-9 {
		t.Fatalf("packet loss = %v; want %.2f", summary.PacketLossPercent, packetLoss)
	}
}

func TestAssessTestConfidenceHighFixture(t *testing.T) {
	c := New(Config{})
	zeroLoss := 0.0
	results := &Results{
		PacketLoss: &PacketLossResult{
			Sent: 1000, Received: 1000, ForwardSent: 1000, ForwardReceived: 1000,
			ForwardLossPercent: &zeroLoss, AcknowledgementsSent: 1000, AcknowledgementsReceived: 1000,
			ReverseAcknowledgementLossPercent: &zeroLoss, FrameSizeBytes: protocol.PacketFrameSize,
		},
	}
	for index, mbps := range []float64{100, 102, 101} {
		results.ThroughputSamples = append(results.ThroughputSamples,
			ThroughputSample{Direction: "download", Mbps: mbps, Duration: time.Second, SampleKind: "window", Profile: "window", WindowIndex: index, TimingSource: "aggregate-wall-clock"},
			ThroughputSample{Direction: "upload", Mbps: mbps / 2, Duration: time.Second, SampleKind: "window", Profile: "window", WindowIndex: index, TimingSource: "aggregate-wall-clock"},
		)
	}
	for index := 0; index < 12; index++ {
		results.LatencySamples = append(results.LatencySamples, LatencySample{
			Phase: "unloaded", RTT: time.Duration(10+index%2) * time.Millisecond, TimingSource: "httptrace",
		})
	}
	for index := 0; index < 3; index++ {
		results.LatencySamples = append(results.LatencySamples,
			LatencySample{Phase: "download", RTT: time.Duration(15+index) * time.Millisecond, LoadOverlapped: true, LoadTrackingAccurate: true, TimingSource: "httptrace"},
			LatencySample{Phase: "upload", RTT: time.Duration(16+index) * time.Millisecond, LoadOverlapped: true, LoadTrackingAccurate: true, TimingSource: "httptrace"},
		)
	}

	confidence := c.assessTestConfidence(results)
	if confidence.Overall != "high" || confidence.OverallScore != 100 {
		t.Fatalf("confidence = %#v; want high/100", confidence)
	}
	if !confidence.Metrics.SampleCount.Adequate || !confidence.Metrics.Variability.Acceptable ||
		!confidence.Metrics.LoadedOverlap.Complete || !confidence.Metrics.Timing.Accurate ||
		!confidence.Metrics.PacketTest.Completed {
		t.Fatalf("confidence gates = %#v; want all complete", confidence.Metrics)
	}
}

func TestAssessTestConfidenceRequiresBothPacketDirections(t *testing.T) {
	c := New(Config{DownloadOnly: true})
	forwardLoss := 100.0
	results := &Results{
		PacketLoss: &PacketLossResult{
			Sent: 1000, Received: 0, ForwardSent: 1000, ForwardReceived: 0,
			ForwardLossPercent: &forwardLoss, AcknowledgementsSent: 0,
		},
	}
	for index, mbps := range []float64{100, 101, 102} {
		results.ThroughputSamples = append(results.ThroughputSamples, ThroughputSample{
			Direction: "download", Mbps: mbps, Duration: time.Second, SampleKind: "window",
			Profile: "window", WindowIndex: index, TimingSource: "aggregate-wall-clock",
		})
	}
	for index := 0; index < 12; index++ {
		results.LatencySamples = append(results.LatencySamples, LatencySample{
			Phase: "unloaded", RTT: time.Duration(10+index%2) * time.Millisecond, TimingSource: "httptrace",
		})
	}
	for index := 0; index < 3; index++ {
		results.LatencySamples = append(results.LatencySamples, LatencySample{
			Phase: "download", RTT: time.Duration(15+index) * time.Millisecond,
			LoadOverlapped: true, LoadTrackingAccurate: true, TimingSource: "httptrace",
		})
	}

	confidence := c.assessTestConfidence(results)
	if confidence.Metrics.PacketTest.Completed {
		t.Fatalf("packet confidence = %#v; reverse direction was unavailable", confidence.Metrics.PacketTest)
	}
	if confidence.OverallScore != 80 {
		t.Fatalf("confidence score = %d; want 80 after packet-test deduction", confidence.OverallScore)
	}
}

func TestValidatePacketReportRejectsImpossibleCounters(t *testing.T) {
	valid := packetTestReportResponse{
		ForwardReceived: 995, AcknowledgementsSent: 990, AckSendFailures: 5,
		DuplicateFrames: 1, InvalidFrames: 2,
	}
	if err := validatePacketReport(valid, 1000, 988); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}

	tests := []struct {
		name   string
		report packetTestReportResponse
		recv   int
	}{
		{name: "forward exceeds sent", report: packetTestReportResponse{ForwardReceived: 1001, AcknowledgementsSent: 1001}, recv: 1000},
		{name: "ack accounting mismatch", report: packetTestReportResponse{ForwardReceived: 995, AcknowledgementsSent: 990, AckSendFailures: 4}, recv: 988},
		{name: "client ack exceeds daemon", report: valid, recv: 991},
		{name: "negative counter", report: packetTestReportResponse{ForwardReceived: 1, AcknowledgementsSent: 1, DuplicateFrames: -1}, recv: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePacketReport(test.report, 1000, test.recv); err == nil {
				t.Fatalf("report %#v unexpectedly accepted", test.report)
			}
		})
	}
}

func TestPacketLossJSONIncludesDirectionalNullsWhenUnavailable(t *testing.T) {
	encoded, err := json.Marshal(PacketLossResult{Unavailable: true, Reason: "no report"})
	if err != nil {
		t.Fatalf("marshal packet loss: %v", err)
	}
	text := string(encoded)
	for _, field := range []string{
		`"lossPercent":null`,
		`"transactionLossPercent":null`,
		`"forwardLossPercent":null`,
		`"reverseAcknowledgementLossPercent":null`,
	} {
		if !strings.Contains(text, field) {
			t.Fatalf("unavailable JSON = %s; want %s", text, field)
		}
	}
}

func TestRunRequiresPhaseTwoMeasurementProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta" {
			t.Fatalf("unexpected request after metadata negotiation: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Meta{
			MaxTransferBytes:           2_000_000,
			MeasurementProtocolVersion: protocol.MeasurementProtocolVersion - 1,
			UploadReceiptVersion:       protocol.UploadReceiptVersion,
			PacketLossFrameVersion:     protocol.PacketLossFrameVersion,
		})
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	_, err := client.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "measurement protocol") {
		t.Fatalf("Run error = %v; want old measurement protocol rejection", err)
	}
}

func TestDirectionalLossCalculationBoundsCounters(t *testing.T) {
	if got := calculateLossPercent(1000, 995); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("calculateLossPercent = %v; want 0.5", got)
	}
	if got := calculateLossPercent(1000, 1200); got != 0 {
		t.Fatalf("over-counted receive loss = %v; want 0", got)
	}
	if got := calculateLossPercent(1000, -5); got != 100 {
		t.Fatalf("negative receive loss = %v; want 100", got)
	}
}

func TestJSONPreservesFirstWindowAndPreciseLoadTracking(t *testing.T) {
	window := ThroughputSample{
		Timestamp:   time.Unix(0, 0),
		SampleKind:  "window",
		WindowIndex: 0,
	}.ToJSON()
	if window.WindowIndex == nil || *window.WindowIndex != 0 {
		t.Fatalf("first window index = %#v; want explicit 0", window.WindowIndex)
	}

	baseline := ThroughputSample{
		Timestamp:  time.Unix(0, 0),
		SampleKind: "baseline",
	}.ToJSON()
	if baseline.WindowIndex != nil {
		t.Fatalf("baseline window index = %#v; want omitted", baseline.WindowIndex)
	}

	latency := LatencySample{
		Timestamp:            time.Unix(0, 0),
		Phase:                "download",
		LoadOverlapped:       true,
		LoadTrackingAccurate: true,
	}.ToJSON()
	encoded, err := json.Marshal(latency)
	if err != nil {
		t.Fatalf("marshal latency sample: %v", err)
	}
	text := string(encoded)
	for _, field := range []string{`"loadOverlapped":true`, `"loadTrackingAccurate":true`} {
		if !strings.Contains(text, field) {
			t.Fatalf("loaded latency JSON = %s; want %s", text, field)
		}
	}
}

func TestSetRequestHeadersAddsOptionalBearerToken(t *testing.T) {
	client := New(Config{AccessToken: "token-0123456789"})
	request := httptest.NewRequest(http.MethodGet, "http://example.test/meta", nil)
	client.setRequestHeaders(request)
	if got := request.Header.Get("Authorization"); got != "Bearer token-0123456789" {
		t.Fatalf("Authorization=%q; want bearer token", got)
	}

	withoutToken := New(Config{})
	request = httptest.NewRequest(http.MethodGet, "http://example.test/meta", nil)
	withoutToken.setRequestHeaders(request)
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("unexpected Authorization=%q", got)
	}
}

func TestClampLoadConcurrencyReservesProbeSlot(t *testing.T) {
	client := New(Config{})
	client.maxConcurrentTransfersPerClient = 4
	if got := client.clampLoadConcurrency(16); got != 3 {
		t.Fatalf("clamped concurrency=%d; want 3", got)
	}
	if got := client.clampLoadConcurrency(2); got != 2 {
		t.Fatalf("small concurrency=%d; want 2", got)
	}
}

func TestRunRejectsServerTransferCeilingTooLowForLoadedLatency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta" {
			t.Fatalf("unexpected request after metadata negotiation: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Meta{
			MaxTransferBytes:                2_000_000,
			MaxConcurrentTransfersPerClient: 1,
			MeasurementProtocolVersion:      protocol.MeasurementProtocolVersion,
			UploadReceiptVersion:            protocol.UploadReceiptVersion,
			PacketLossFrameVersion:          protocol.PacketLossFrameVersion,
		})
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	_, err := client.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("Run error=%v; want transfer-ceiling rejection", err)
	}
}
