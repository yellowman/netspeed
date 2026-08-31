package cloudflarecompat

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloudflareMeasurementRequestsDisableTransformation(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]http.Header)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		mu.Lock()
		seen[request.Method] = request.Header.Clone()
		mu.Unlock()
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	o := options{Server: server.URL, Timeout: 3 * time.Second}
	client := newHTTPClient(o)
	for _, test := range []struct {
		method string
		body   io.Reader
		length int64
	}{
		{method: http.MethodGet, length: -1},
		{method: http.MethodPost, body: strings.NewReader("0000"), length: 4},
	} {
		response, err := request(context.Background(), client, o, test.method, "/measure", nil, test.body, test.length)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		header := seen[method]
		if got := header.Get("Accept-Encoding"); got != "identity" {
			t.Fatalf("%s Accept-Encoding=%q", method, got)
		}
		if got := header.Get("Cache-Control"); got != "no-store, no-transform" {
			t.Fatalf("%s Cache-Control=%q", method, got)
		}
		if got := header.Get("Pragma"); got != "no-cache" {
			t.Fatalf("%s Pragma=%q", method, got)
		}
	}
	if got := seen[http.MethodPost].Get("Content-Encoding"); got != "identity" {
		t.Fatalf("POST Content-Encoding=%q", got)
	}
}

func TestCloudflareTransportProbeObservesDefaultsAndDoesNotSendDiscriminators(t *testing.T) {
	payload := make([]byte, cloudflareTransportProbeBytes)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	var observedQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedQuery = request.URL.Query()
		writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		writer.Header().Set("Cache-Control", "no-store, no-transform")
		writer.Header().Set("X-Accel-Buffering", "no")
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	o := options{
		Server:            server.URL,
		Timeout:           3 * time.Second,
		DownloadPayload:   "random",
		DownloadFraming:   "fixed",
		DownloadFlush:     "auto",
		TransportControls: true,
	}
	summary, err := probeAndNegotiateCloudflareTransport(context.Background(), newHTTPClient(o), o)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Selection.DownloadPayload != "random" || summary.Selection.DownloadFraming != "fixed" {
		t.Fatalf("selection=%+v", summary.Selection)
	}
	if summary.QueryDiscriminatorsSent || !summary.ProviderDefaultsOnly {
		t.Fatalf("unsafe negotiation summary=%+v", summary)
	}
	for _, forbidden := range []string{"payload", "pattern", "fill", "framing", "stream", "chunkBytes", "flush"} {
		if observedQuery.Has(forbidden) {
			t.Fatalf("probe sent unsupported discriminator %q in %v", forbidden, observedQuery)
		}
	}
	if observedQuery.Get("bytes") != strconv.Itoa(cloudflareTransportProbeBytes) {
		t.Fatalf("bytes query=%q", observedQuery.Get("bytes"))
	}
	if !summary.AntiTransform.ResponseNoStore || !summary.AntiTransform.ResponseNoTransform || !summary.AntiTransform.ProxyBufferSuppressionObserved {
		t.Fatalf("anti-transform evidence=%+v", summary.AntiTransform)
	}
}

func TestCloudflareTransportProbeAcceptsVerifiedChunkAndFlushDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count, _ := strconv.Atoi(request.URL.Query().Get("bytes"))
		writer.Header().Set("Content-Length", strconv.Itoa(count))
		writer.Header().Set("X-Netspeed-Chunk-Bytes", "4096")
		writer.Header().Set("X-Netspeed-Flush", "false")
		_, _ = writer.Write(make([]byte, count))
	}))
	defer server.Close()

	o := options{
		Server:             server.URL,
		Timeout:            3 * time.Second,
		DownloadPayload:    "zero",
		DownloadFraming:    "fixed",
		DownloadChunkBytes: 4096,
		DownloadFlush:      "false",
		TransportControls:  true,
	}
	summary, err := probeAndNegotiateCloudflareTransport(context.Background(), newHTTPClient(o), o)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Selection.DownloadChunkBytes == nil || *summary.Selection.DownloadChunkBytes != 4096 {
		t.Fatalf("chunk selection=%+v", summary.Selection)
	}
	if summary.Selection.DownloadFlush == nil || *summary.Selection.DownloadFlush {
		t.Fatalf("flush selection=%+v", summary.Selection)
	}
}

func TestCloudflareTransportProbeRejectsUnverifiableChunkControl(t *testing.T) {
	server := fixedZeroDownloadServer(t)
	defer server.Close()
	o := options{Server: server.URL, Timeout: 3 * time.Second, DownloadPayload: "auto", DownloadFraming: "auto", DownloadChunkBytes: 4096, DownloadFlush: "auto"}
	_, err := probeAndNegotiateCloudflareTransport(context.Background(), newHTTPClient(o), o)
	var controlErr *transportControlError
	if !errors.As(err, &controlErr) || !strings.Contains(err.Error(), "did not advertise") {
		t.Fatalf("error=%v; want transport-control error", err)
	}
}

func TestCloudflareTransportRejectsEncodedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Encoding", "gzip")
		writer.Header().Set("Content-Length", strconv.Itoa(cloudflareTransportProbeBytes))
		_, _ = writer.Write(make([]byte, cloudflareTransportProbeBytes))
	}))
	defer server.Close()
	o := options{Server: server.URL, Timeout: 3 * time.Second}
	_, err := probeAndNegotiateCloudflareTransport(context.Background(), newHTTPClient(o), o)
	if err == nil || !strings.Contains(err.Error(), "Content-Encoding") {
		t.Fatalf("error=%v; want encoded-response rejection", err)
	}
}

func TestCloudflareWarmLatencyRequiresReusedConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("bytes") != "0" {
			t.Errorf("latency bytes=%q", request.URL.Query().Get("bytes"))
		}
		writer.Header().Set("Server-Timing", "app;dur=0.001")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	session := newCloudflareLatencySession(options{Server: server.URL, Timeout: 3 * time.Second})
	defer session.Close()
	if err := session.Prime(context.Background(), "idle"); err != nil {
		t.Fatal(err)
	}
	values := make([]float64, 0, 3)
	for sequence := 0; sequence < 3; sequence++ {
		value, err := session.Probe(context.Background(), "idle", sequence)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	summary := session.Summarize(values, 3, "insufficient")
	if !summary.Available || !summary.ConnectionReused || summary.WarmSamples != 3 || summary.WarmupRequests != 1 || summary.DiscardedColdAttempts != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if len(summary.HTTPProtocols) != 1 || summary.HTTPProtocols[0] != "HTTP/1.1" {
		t.Fatalf("protocol evidence=%v", summary.HTTPProtocols)
	}
}

func TestCloudflareWarmLatencyDiscardsColdConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Connection", "close")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	session := newCloudflareLatencySession(options{Server: server.URL, Timeout: 3 * time.Second})
	defer session.Close()
	if err := session.Prime(context.Background(), "idle"); err != nil {
		t.Fatal(err)
	}
	_, err := session.Probe(context.Background(), "idle", 0)
	if err == nil || !strings.Contains(err.Error(), "not reused") {
		t.Fatalf("error=%v; want reuse failure", err)
	}
	if session.discardedColdAttempts != cloudflareLatencyAttempts {
		t.Fatalf("discarded=%d want=%d", session.discardedColdAttempts, cloudflareLatencyAttempts)
	}
}

func TestCloudflareDownloadDetectsPayloadDrift(t *testing.T) {
	randomPayload := make([]byte, 1<<20)
	if _, err := rand.Read(randomPayload); err != nil {
		t.Fatal(err)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		count, _ := strconv.Atoi(request.URL.Query().Get("bytes"))
		writer.Header().Set("Content-Length", strconv.Itoa(count))
		if requests == 1 {
			_, _ = writer.Write(make([]byte, count))
			return
		}
		_, _ = writer.Write(randomPayload[:count])
	}))
	defer server.Close()

	o := options{Server: server.URL, Timeout: 3 * time.Second}
	client := newHTTPClient(o)
	transport, err := probeAndNegotiateCloudflareTransport(context.Background(), client, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Transport = &transport
	_, _, err = transferOnce(context.Background(), client, o, false, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "payload drifted") {
		t.Fatalf("error=%v; want payload-drift rejection", err)
	}
}

func TestCloudflareDownloadSamplesAcrossBodyForCompressibleTail(t *testing.T) {
	randomPrefix := make([]byte, cloudflareTransportProbeBytes)
	if _, err := rand.Read(randomPrefix); err != nil {
		t.Fatal(err)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		count, _ := strconv.Atoi(request.URL.Query().Get("bytes"))
		writer.Header().Set("Content-Length", strconv.Itoa(count))
		if requests == 1 {
			_, _ = writer.Write(randomPrefix[:count])
			return
		}
		prefixBytes := minInt64(int64(len(randomPrefix)), int64(count))
		_, _ = writer.Write(randomPrefix[:prefixBytes])
		_, _ = writer.Write(make([]byte, int64(count)-prefixBytes))
	}))
	defer server.Close()

	o := options{Server: server.URL, Timeout: 3 * time.Second}
	client := newHTTPClient(o)
	transport, err := probeAndNegotiateCloudflareTransport(context.Background(), client, o)
	if err != nil {
		t.Fatal(err)
	}
	if transport.Selection.DownloadPayload != "random" {
		t.Fatalf("probe payload=%q want random", transport.Selection.DownloadPayload)
	}
	o.Transport = &transport
	_, _, err = transferOnce(context.Background(), client, o, false, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "payload drifted") {
		t.Fatalf("error=%v; want distributed payload-drift rejection", err)
	}
}

func TestCloudflareDownloadDetectsFramingDrift(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		count, _ := strconv.Atoi(request.URL.Query().Get("bytes"))
		if requests == 1 {
			writer.Header().Set("Content-Length", strconv.Itoa(count))
			_, _ = writer.Write(make([]byte, count))
			return
		}
		writer.Header().Set("X-Netspeed-Framing", "chunked")
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("fixture response writer does not support flushing")
			return
		}
		first := count / 2
		_, _ = writer.Write(make([]byte, first))
		flusher.Flush()
		_, _ = writer.Write(make([]byte, count-first))
	}))
	defer server.Close()

	o := options{Server: server.URL, Timeout: 3 * time.Second}
	client := newHTTPClient(o)
	transport, err := probeAndNegotiateCloudflareTransport(context.Background(), client, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Transport = &transport
	_, _, err = transferOnce(context.Background(), client, o, false, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "framing drifted") {
		t.Fatalf("error=%v; want framing-drift rejection", err)
	}
}

func TestCloudflareDownloadDetectsAntiTransformEvidenceDrift(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		count, _ := strconv.Atoi(request.URL.Query().Get("bytes"))
		writer.Header().Set("Content-Length", strconv.Itoa(count))
		if requests == 1 {
			writer.Header().Set("Cache-Control", "no-store, no-transform")
			writer.Header().Set("X-Accel-Buffering", "no")
		}
		_, _ = writer.Write(make([]byte, count))
	}))
	defer server.Close()

	o := options{Server: server.URL, Timeout: 3 * time.Second}
	client := newHTTPClient(o)
	transport, err := probeAndNegotiateCloudflareTransport(context.Background(), client, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Transport = &transport
	_, _, err = transferOnce(context.Background(), client, o, false, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "lost the probed Cache-Control no-store") {
		t.Fatalf("error=%v; want anti-transform-evidence rejection", err)
	}
}

func TestPrioritizedServerDurationRecognizesCloudflareMetricFamilies(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   float64
	}{
		{name: "short request duration", header: "app;dur=7, cfReqDur;dur=2, cfSpeedApp;dur=4", want: 2},
		{name: "long request duration", header: "cfRequestDuration;dur=3.5, cfSpeedApp;dur=4", want: 3.5},
		{name: "Cloudflare speed components", header: "cfSpeed;dur=4, cfSpeedApp;dur=6", want: 10},
		{name: "application fallback", header: "cache;dur=1, app;dur=7", want: 7},
		{name: "sub-resolution duration ignored", header: "cfReqDur;dur=0.001", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := prioritizedServerDurationMS(test.header); got != test.want {
				t.Fatalf("duration=%v want=%v", got, test.want)
			}
		})
	}
}

func fixedZeroDownloadServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		count, err := strconv.Atoi(request.URL.Query().Get("bytes"))
		if err != nil {
			t.Errorf("bytes query: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Length", strconv.Itoa(count))
		_, _ = writer.Write(make([]byte, count))
	}))
}
