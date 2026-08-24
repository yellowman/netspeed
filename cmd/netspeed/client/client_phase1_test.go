package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/measurement"
)

func TestProfilesWithinLimit(t *testing.T) {
	profiles := []profile{
		{Name: "small", Bytes: 100, Runs: 1},
		{Name: "exact", Bytes: 200, Runs: 1},
		{Name: "large", Bytes: 201, Runs: 1},
	}

	got := profilesWithinLimit(profiles, 200)
	if len(got) != 2 || got[0].Name != "small" || got[1].Name != "exact" {
		t.Fatalf("profilesWithinLimit() = %#v", got)
	}
}

func TestGeneratedUploadBodyStreamsExactSize(t *testing.T) {
	body := newGeneratedUploadBody(10)
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(data) != 10 {
		t.Fatalf("read %d bytes, want 10", len(data))
	}
	for i, value := range data {
		if value != 0 {
			t.Fatalf("byte %d = %d, want 0", i, value)
		}
	}
	readBytes, firstRead, lastRead := body.snapshot()
	if readBytes != 10 || firstRead.IsZero() || lastRead.IsZero() {
		t.Fatalf("snapshot = bytes:%d first:%v last:%v", readBytes, firstRead, lastRead)
	}
}

func TestFetchMetaRequiresCapabilityLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Meta{MeasurementAPIVersion: measurement.APIVersion})
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	if _, err := client.fetchMeta(context.Background()); err == nil {
		t.Fatal("fetchMeta unexpectedly accepted API v1 without maxTransferBytes")
	}
}

func TestFetchMetaResetsNegotiatedCapabilities(t *testing.T) {
	responses := []Meta{
		{MeasurementAPIVersion: measurement.APIVersion, MaxTransferBytes: 1234},
		{MaxTransferBytes: 567},
		{},
	}
	requestIndex := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requestIndex >= len(responses) {
			t.Fatal("unexpected metadata request")
		}
		_ = json.NewEncoder(w).Encode(responses[requestIndex])
		requestIndex++
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()

	if _, err := client.fetchMeta(context.Background()); err != nil {
		t.Fatalf("fetch v1 meta: %v", err)
	}
	if client.measurementAPIVersion != measurement.APIVersion || client.maxTransferBytes != 1234 {
		t.Fatalf("v1 negotiation = version:%d max:%d", client.measurementAPIVersion, client.maxTransferBytes)
	}

	if _, err := client.fetchMeta(context.Background()); err != nil {
		t.Fatalf("fetch capability-aware legacy meta: %v", err)
	}
	if client.measurementAPIVersion != 0 || client.maxTransferBytes != 567 {
		t.Fatalf("legacy negotiation = version:%d max:%d", client.measurementAPIVersion, client.maxTransferBytes)
	}

	if _, err := client.fetchMeta(context.Background()); err != nil {
		t.Fatalf("fetch plain legacy meta: %v", err)
	}
	if client.measurementAPIVersion != 0 || client.maxTransferBytes != LegacyMaxTransferBytes {
		t.Fatalf("plain legacy negotiation = version:%d max:%d", client.measurementAPIVersion, client.maxTransferBytes)
	}
}

func TestMeasureDownloadRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	client.maxTransferBytes = 100
	if _, err := client.measureDownload(context.Background(), "test", 10, 0); err == nil {
		t.Fatal("measureDownload unexpectedly accepted HTTP 500")
	}
}

func TestMeasureDownloadRejectsTruncatedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "short")
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	client.maxTransferBytes = 100
	if _, err := client.measureDownload(context.Background(), "test", 10, 0); err == nil {
		t.Fatal("measureDownload unexpectedly accepted a truncated body")
	}
}

func TestMeasureUploadVerifiesReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(measurement.UploadReceipt{
			OK:               true,
			AcceptedBytes:    n,
			ServerDurationNS: int64(time.Millisecond),
		})
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	client.maxTransferBytes = 10_000
	client.measurementAPIVersion = measurement.APIVersion

	sample, err := client.measureUpload(context.Background(), "test", 4096, 0)
	if err != nil {
		t.Fatalf("measureUpload: %v", err)
	}
	if sample.SizeBytes != 4096 || sample.Duration != time.Millisecond {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestMeasureUploadRejectsReceiptMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(measurement.UploadReceipt{
			OK:               true,
			AcceptedBytes:    1,
			ServerDurationNS: int64(time.Millisecond),
		})
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	client.maxTransferBytes = 10_000
	client.measurementAPIVersion = measurement.APIVersion

	if _, err := client.measureUpload(context.Background(), "test", 4096, 0); err == nil {
		t.Fatal("measureUpload unexpectedly accepted a mismatched receipt")
	}
}

func TestMeasureUploadRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	client.maxTransferBytes = 10_000
	client.measurementAPIVersion = measurement.APIVersion
	if _, err := client.measureUpload(context.Background(), "test", 4096, 0); err == nil {
		t.Fatal("measureUpload unexpectedly accepted HTTP 500")
	}
}

func TestRunProfilesRequiresMinimumValidSamples(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New(Config{ServerURL: server.URL})
	client.httpClient = server.Client()
	client.maxTransferBytes = 100
	_, err := client.runProfiles(context.Background(), []profile{{Name: "test", Bytes: 10, Runs: 3}}, "download")
	if err == nil || !strings.Contains(err.Error(), "need at least 2") {
		t.Fatalf("runProfiles error = %v", err)
	}
}

func TestSummaryLeavesUnavailableMeasurementsNull(t *testing.T) {
	client := &Client{}
	results := &Results{
		ThroughputSamples: []ThroughputSample{{Direction: "download", Mbps: 25}},
		LatencySamples: []LatencySample{
			{Phase: "unloaded", RTT: 10 * time.Millisecond},
			{Phase: "unloaded", RTT: 11 * time.Millisecond},
		},
		PacketLoss: &PacketLossResult{Unavailable: true, Reason: "not measured"},
	}

	summary := client.calculateSummary(results)
	if summary.DownloadMbps == nil || *summary.DownloadMbps != 25 {
		t.Fatalf("download summary = %v", summary.DownloadMbps)
	}
	if summary.UploadMbps != nil || summary.PacketLossPercent != nil {
		t.Fatalf("unavailable values should be nil: %#v", summary)
	}
	quality := client.calculateQuality(summary)
	if quality.VideoStreaming != "N/A" || quality.Gaming != "N/A" || quality.VideoChatting != "N/A" {
		t.Fatalf("quality = %#v, want all N/A", quality)
	}
}

func TestRequireStatusOKIncludesBoundedErrorDetail(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader("bad request")),
	}
	if err := requireStatusOK(resp, "test"); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("requireStatusOK error = %v", err)
	}
}
