package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yellowman/netspeed/internal/measurementhttp"
	"github.com/yellowman/netspeed/internal/protocol"
)

func clientTransportCapabilities() *measurementhttp.Capabilities {
	return &measurementhttp.Capabilities{
		Version:                       measurementhttp.TransportVersion,
		DownloadPath:                  "/measure/down",
		DownloadBytesParameter:        "n",
		DownloadPayloadParameter:      "kind",
		DownloadFramingParameter:      "frame",
		DownloadChunkBytesParameter:   "chunk",
		DownloadFlushParameter:        "flushNow",
		UploadPath:                    "/measure/up",
		UploadBytesParameter:          "n",
		HTTPPingPath:                  "/measure/ping",
		HTTPPingMethods:               []string{http.MethodHead, http.MethodGet},
		WarmConnectionPing:            true,
		DownloadPayloads:              []string{"random", "zero"},
		DownloadFramings:              []string{"fixed", "chunked"},
		DefaultDownloadPayload:        "random",
		DefaultDownloadFraming:        "fixed",
		DefaultChunkBytes:             64 << 10,
		MinimumChunkBytes:             4 << 10,
		MaximumChunkBytes:             1 << 20,
		UploadContentEncodings:        []string{"identity"},
		ResponseCacheControl:          measurementhttp.CacheControl,
		NoTransform:                   true,
		ProxyBufferSuppressionHeader:  "X-Accel-Buffering: no",
		ProxyRequestBufferingAdvisory: true,
	}
}

func selectedClientTransport(t *testing.T) measurementhttp.Selection {
	t.Helper()
	selection, err := measurementhttp.Negotiate(clientTransportCapabilities(), measurementhttp.Preferences{
		DownloadPayload:    "zero",
		DownloadFraming:    "chunked",
		DownloadChunkBytes: 4096,
		DownloadFlush:      "false",
	})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	return selection
}

func TestMeasurementURLsUseAdvertisedPathsAndParameterNames(t *testing.T) {
	client := New(Config{ServerURL: "https://speed.example.test/base"})
	client.measurementTransport = selectedClientTransport(t)

	downloadURL, err := url.Parse(client.downloadMeasurementURL(8192, url.Values{"measId": {"download-id"}}))
	if err != nil {
		t.Fatalf("parse download URL: %v", err)
	}
	if downloadURL.Path != "/base/measure/down" {
		t.Fatalf("download path = %q; want /base/measure/down", downloadURL.Path)
	}
	for key, want := range map[string]string{
		"n":        "8192",
		"kind":     "zero",
		"frame":    "chunked",
		"chunk":    "4096",
		"flushNow": "false",
		"measId":   "download-id",
	} {
		if got := downloadURL.Query().Get(key); got != want {
			t.Fatalf("download query %s = %q; want %q", key, got, want)
		}
	}
	if got := downloadURL.Query().Get("bytes"); got != "" {
		t.Fatalf("hard-coded bytes parameter leaked into negotiated URL: %q", got)
	}

	uploadURL, err := url.Parse(client.uploadMeasurementURL(4096, url.Values{"measId": {"upload-id"}}))
	if err != nil {
		t.Fatalf("parse upload URL: %v", err)
	}
	if uploadURL.Path != "/base/measure/up" || uploadURL.Query().Get("n") != "4096" {
		t.Fatalf("upload URL = %s; want advertised path and byte parameter", uploadURL)
	}

	method, latencyURL := client.latencyMeasurementRequest(url.Values{"seq": {"7"}})
	parsedLatency, err := url.Parse(latencyURL)
	if err != nil {
		t.Fatalf("parse latency URL: %v", err)
	}
	if method != http.MethodGet || parsedLatency.Path != "/base/measure/ping" || parsedLatency.Query().Get("seq") != "7" {
		t.Fatalf("latency request = %s %s; want GET advertised ping path", method, latencyURL)
	}
	if parsedLatency.Query().Get("n") != "" {
		t.Fatalf("dedicated latency URL unexpectedly includes download byte discriminator: %s", latencyURL)
	}
}

func TestNegotiatedDownloadAndUploadVerifyTransportContract(t *testing.T) {
	selection := selectedClientTransport(t)
	var downloadRequests atomic.Int64
	var uploadRequests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q; want identity", request.Header.Get("Accept-Encoding"))
		}
		if cacheControl := request.Header.Get("Cache-Control"); !headerHasDirective(cacheControl, "no-store") || !headerHasDirective(cacheControl, "no-transform") {
			t.Errorf("request Cache-Control = %q; want no-store, no-transform", cacheControl)
		}
		switch request.URL.Path {
		case "/measure/down":
			downloadRequests.Add(1)
			for key, want := range map[string]string{
				"n":        "8192",
				"kind":     "zero",
				"frame":    "chunked",
				"chunk":    "4096",
				"flushNow": "false",
			} {
				if got := request.URL.Query().Get(key); got != want {
					t.Errorf("download query %s = %q; want %q", key, got, want)
				}
			}
			measurementhttp.SetResponseHeaders(writer.Header(), "download")
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("X-Netspeed-Payload", "zero")
			writer.Header().Set("X-Netspeed-Framing", "chunked")
			writer.Header().Set("X-Netspeed-Chunk-Bytes", "4096")
			writer.Header().Set("X-Netspeed-Flush", "false")
			writer.WriteHeader(http.StatusOK)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = io.CopyN(writer, zeroReader{}, 8192)
		case "/measure/up":
			uploadRequests.Add(1)
			if got := request.URL.Query().Get("n"); got != "4096" {
				t.Errorf("upload bytes query = %q; want 4096", got)
			}
			if got := request.Header.Get("Content-Encoding"); got != "identity" {
				t.Errorf("Content-Encoding = %q; want identity", got)
			}
			accepted, err := io.Copy(io.Discard, request.Body)
			if err != nil {
				t.Errorf("read upload: %v", err)
			}
			measurementhttp.SetResponseHeaders(writer.Header(), "upload")
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("X-Netspeed-Payload", "discarded")
			writer.Header().Set("X-Netspeed-Framing", "fixed")
			writer.Header().Set("X-Netspeed-Content-Encoding", "identity")
			writer.Header().Set("X-Netspeed-Expected-Bytes", "4096")
			writer.Header().Set("X-Netspeed-Accepted-Bytes", strconv.FormatInt(accepted, 10))
			writer.Header().Set("X-Netspeed-Upload-Duration-Ns", "1000")
			_ = json.NewEncoder(writer).Encode(protocol.UploadReceipt{
				OK:               true,
				AcceptedBytes:    accepted,
				ServerDurationNS: 1000,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := testClient(server)
	client.measurementTransport = selection

	download, err := client.measureDownload(context.Background(), "transport", 8192, 0)
	if err != nil {
		t.Fatalf("measureDownload: %v", err)
	}
	if download.SizeBytes != 8192 || download.Mbps <= 0 {
		t.Fatalf("download sample = %#v", download)
	}

	upload, err := client.measureUpload(context.Background(), "transport", 4096, 0)
	if err != nil {
		t.Fatalf("measureUpload: %v", err)
	}
	if upload.SizeBytes != 4096 || upload.Mbps <= 0 {
		t.Fatalf("upload sample = %#v", upload)
	}
	if downloadRequests.Load() != 1 || uploadRequests.Load() != 1 {
		t.Fatalf("request counts: download=%d upload=%d", downloadRequests.Load(), uploadRequests.Load())
	}
}

func TestWarmLatencyRetriesColdConnectionAndLabelsSample(t *testing.T) {
	selection := selectedClientTransport(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/measure/ping" {
			t.Errorf("latency path = %q; want /measure/ping", request.URL.Path)
		}
		requests.Add(1)
		measurementhttp.SetResponseHeaders(writer.Header(), "latency")
		writer.Header().Set("Content-Length", "0")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testClient(server)
	client.measurementTransport = selection
	sample, err := client.measureLatencySample(context.Background(), "unloaded", 1)
	if err != nil {
		t.Fatalf("measureLatencySample: %v", err)
	}
	if !sample.ConnectionReused {
		t.Fatal("reported latency sample used a cold connection")
	}
	if sample.ProbeTransport != "http" || sample.ProbeMethod != http.MethodGet || sample.ProbePath != "/measure/ping" {
		t.Fatalf("latency labels = %#v", sample)
	}
	if requests.Load() < 2 {
		t.Fatalf("latency requests = %d; want a discarded cold probe plus a reused probe", requests.Load())
	}
	encoded, err := json.Marshal(sample.ToJSON())
	if err != nil {
		t.Fatalf("marshal latency sample: %v", err)
	}
	for _, field := range []string{`"connectionReused":true`, `"probeTransport":"http"`, `"probeMethod":"GET"`, `"probePath":"/measure/ping"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("latency JSON = %s; want %s", encoded, field)
		}
	}
}

func TestNegotiatedResponseRejectsTransformationOrWrongDiscriminator(t *testing.T) {
	client := New(Config{})
	client.measurementTransport = selectedClientTransport(t)

	response := &http.Response{
		Header: http.Header{
			"Cache-Control":          []string{"no-store"},
			"X-Netspeed-Measurement": []string{"download"},
		},
	}
	if err := client.verifyCommonMeasurementResponse(response, "download"); err == nil || !strings.Contains(err.Error(), "no-transform") {
		t.Fatalf("verifyCommonMeasurementResponse error = %v; want transformation rejection", err)
	}

	response.Header.Set("Cache-Control", measurementhttp.CacheControl)
	response.Header.Set("X-Netspeed-Payload", "random")
	response.Header.Set("X-Netspeed-Framing", "chunked")
	response.Header.Set("X-Netspeed-Chunk-Bytes", "4096")
	response.Header.Set("X-Netspeed-Flush", "false")
	response.ContentLength = -1
	response.ProtoMajor = 1
	response.TransferEncoding = []string{"chunked"}
	if err := client.verifyDownloadMeasurementResponse(response, 4096, "download"); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("verifyDownloadMeasurementResponse error = %v; want payload mismatch rejection", err)
	}
}
