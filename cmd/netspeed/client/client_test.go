package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSelectProfilesRespectsServerLimit(t *testing.T) {
	all := []profile{
		{Name: "small", Bytes: 100, Runs: 1},
		{Name: "medium", Bytes: 1_000, Runs: 1},
		{Name: "large", Bytes: 10_000, Runs: 1},
	}
	baseline := []profile{{Name: "small", Bytes: 100, Runs: 1}}

	got := selectProfiles(1_000_000, all, baseline, 1_000)
	if len(got) != 2 || got[0].Name != "small" || got[1].Name != "medium" {
		t.Fatalf("selectProfiles returned %#v; want small and medium", got)
	}
}

func TestRequiredSuccessfulRuns(t *testing.T) {
	for total, want := range map[int]int{0: 0, 1: 1, 2: 1, 3: 2, 4: 2, 5: 3} {
		if got := requiredSuccessfulRuns(total); got != want {
			t.Fatalf("requiredSuccessfulRuns(%d) = %d; want %d", total, got, want)
		}
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
