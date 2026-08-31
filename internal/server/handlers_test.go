package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/clientaddr"
	"github.com/yellowman/netspeed/internal/config"
	"github.com/yellowman/netspeed/internal/limits"
	"github.com/yellowman/netspeed/internal/measurementhttp"
	"github.com/yellowman/netspeed/internal/meta"
	"github.com/yellowman/netspeed/internal/protocol"
	"github.com/yellowman/netspeed/internal/websocketping"
)

type panicReadCloser struct{}

func (panicReadCloser) Read([]byte) (int, error) {
	panic("oversized known-length upload should be rejected before reading")
}
func (panicReadCloser) Close() error { return nil }

type errorReadCloser struct {
	data []byte
	err  error
}

func (r *errorReadCloser) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}
func (*errorReadCloser) Close() error { return nil }

func measurementTestServer(maxBytes int64) *Server {
	cfg := config.Default()
	cfg.MaxBytes = maxBytes
	resolver, err := clientaddr.NewResolver(nil)
	if err != nil {
		panic(err)
	}
	return &Server{
		cfg:                   cfg,
		metaProvider:          &meta.StaticProvider{Hostname: "test", Colo: "TST", ClientAddress: resolver},
		clientAddress:         resolver,
		transferLimiter:       limits.NewTransferLimiter(cfg.MaxConcurrentTransfers, cfg.MaxConcurrentTransfersPerClient),
		bandwidthQuota:        limits.NewByteQuota(cfg.ClientBandwidthQuotaBytes, cfg.ClientBandwidthQuotaWindow),
		offerRateLimiter:      limits.NewKeyedRateLimiter(float64(cfg.WebRTCOfferRatePerMinute)/60.0, cfg.WebRTCOfferBurst),
		turnCredentialLimiter: limits.NewKeyedRateLimiter(float64(cfg.TurnCredentialRatePerMinute)/60.0, cfg.TurnCredentialBurst),
		metrics:               &serviceMetrics{},
	}
}

func TestHandleMetaAdvertisesMeasurementCapabilities(t *testing.T) {
	s := measurementTestServer(12345)
	req := httptest.NewRequest(http.MethodGet, "/meta", nil)
	rec := httptest.NewRecorder()

	s.handleMeta(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
	var got meta.ClientMeta
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if got.MaxTransferBytes != 12345 {
		t.Fatalf("maxTransferBytes = %d; want 12345", got.MaxTransferBytes)
	}
	if got.MaxConcurrentTransfersPerClient != s.cfg.MaxConcurrentTransfersPerClient {
		t.Fatalf("maxConcurrentTransfersPerClient = %d; want %d", got.MaxConcurrentTransfersPerClient, s.cfg.MaxConcurrentTransfersPerClient)
	}
	if got.MeasurementProtocolVersion != protocol.MeasurementProtocolVersion {
		t.Fatalf("measurementProtocolVersion = %d; want %d", got.MeasurementProtocolVersion, protocol.MeasurementProtocolVersion)
	}
	if got.UploadReceiptVersion != protocol.UploadReceiptVersion {
		t.Fatalf("uploadReceiptVersion = %d; want %d", got.UploadReceiptVersion, protocol.UploadReceiptVersion)
	}
	if got.PacketLossFrameVersion != protocol.PacketLossFrameVersion {
		t.Fatalf("packetLossFrameVersion = %d; want %d", got.PacketLossFrameVersion, protocol.PacketLossFrameVersion)
	}
	if got.MeasurementCapabilities == nil || got.MeasurementCapabilities.HTTPPingPath != "/__ping" ||
		got.MeasurementCapabilities.WebSocketPingPath != "/__ws" ||
		got.MeasurementCapabilities.WebSocketPingProtocol != measurementhttp.WebSocketPingSubprotocol ||
		got.MeasurementCapabilities.WebSocketPingPayloadBytes != measurementhttp.WebSocketPingPayloadBytes ||
		len(got.MeasurementCapabilities.DownloadPayloads) != 2 || len(got.MeasurementCapabilities.DownloadFramings) != 2 ||
		!got.MeasurementCapabilities.NoTransform || got.MeasurementCapabilities.ProxyBufferSuppressionHeader != "X-Accel-Buffering: no" ||
		got.MeasurementCapabilities.DownloadPayloadParameter != "payload" || got.MeasurementCapabilities.DownloadFramingParameter != "framing" ||
		len(got.MeasurementCapabilities.HTTPPingMethods) != 2 || !got.MeasurementCapabilities.WarmConnectionPing {
		t.Fatalf("measurementCapabilities = %#v; want HTTP transport discriminators", got.MeasurementCapabilities)
	}
}

func TestHandleWebSocketPingEchoesPersistentBinaryNonces(t *testing.T) {
	s := measurementTestServer(1024)
	server := httptest.NewServer(http.HandlerFunc(s.handleWebSocketPing))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := websocketping.Dial(ctx, server.URL, "/__ws", "", "netspeedd-handler-test", time.Second)
	if err != nil {
		t.Fatalf("dial WebSocket ping: %v", err)
	}
	for sequence := uint32(1); sequence <= 2; sequence++ {
		payload, err := websocketping.NewPayload(sequence)
		if err != nil {
			t.Fatalf("create WebSocket payload: %v", err)
		}
		if rtt, err := client.Ping(ctx, payload); err != nil || rtt <= 0 {
			t.Fatalf("WebSocket ping %d RTT=%s error=%v", sequence, rtt, err)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close WebSocket ping: %v", err)
	}
}

func TestHandleUpReturnsVerifiedReceipt(t *testing.T) {
	s := measurementTestServer(16)
	req := httptest.NewRequest(http.MethodPost, "/__up?bytes=4&measId=test", bytes.NewReader([]byte("1234")))
	rec := httptest.NewRecorder()

	s.handleUp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q; want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("Cache-Control = %q; want no-store, no-transform", got)
	}
	var receipt protocol.UploadReceipt
	if err := json.NewDecoder(rec.Body).Decode(&receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if !receipt.OK || receipt.AcceptedBytes != 4 || receipt.ServerDurationNS <= 0 {
		t.Fatalf("receipt = %#v; want verified 4-byte receipt", receipt)
	}
	if rec.Header().Get("X-Netspeed-Accepted-Bytes") != "4" ||
		rec.Header().Get("X-Netspeed-Expected-Bytes") != "4" ||
		rec.Header().Get("X-Netspeed-Content-Encoding") != "identity" ||
		rec.Header().Get("X-Netspeed-Framing") != "fixed" ||
		rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("upload headers=%v", rec.Header())
	}
}

func TestHandleUpRejectsKnownOversizeBeforeReading(t *testing.T) {
	s := measurementTestServer(4)
	req := httptest.NewRequest(http.MethodPost, "/__up", nil)
	req.Body = panicReadCloser{}
	req.ContentLength = 5
	rec := httptest.NewRecorder()

	s.handleUp(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", rec.Code)
	}
}

func TestHandleUpRejectsUnknownLengthOversize(t *testing.T) {
	s := measurementTestServer(4)
	req := httptest.NewRequest(http.MethodPost, "/__up", bytes.NewReader([]byte("12345")))
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	s.handleUp(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", rec.Code)
	}
}

func TestHandleUpRejectsTruncatedKnownLength(t *testing.T) {
	s := measurementTestServer(8)
	req := httptest.NewRequest(http.MethodPost, "/__up", bytes.NewReader([]byte("123")))
	req.ContentLength = 5
	rec := httptest.NewRecorder()

	s.handleUp(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

func TestHandleUpRejectsBodyReadFailure(t *testing.T) {
	boom := errors.New("boom")
	s := measurementTestServer(8)
	req := httptest.NewRequest(http.MethodPost, "/__up", nil)
	req.Body = &errorReadCloser{data: []byte("12"), err: boom}
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	s.handleUp(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "failed to read complete upload") {
		t.Fatalf("body = %q; want read-failure message", rec.Body.String())
	}
}

func TestHandleDownSupportsPayloadAndFramingDiscriminators(t *testing.T) {
	s := measurementTestServer(16 << 10)

	zeroRequest := httptest.NewRequest(http.MethodGet, "/__down?bytes=8192&payload=zero&framing=fixed&chunkBytes=4096", nil)
	zeroRecorder := httptest.NewRecorder()
	s.handleDown(zeroRecorder, zeroRequest)
	if zeroRecorder.Code != http.StatusOK {
		t.Fatalf("zero status=%d body=%q", zeroRecorder.Code, zeroRecorder.Body.String())
	}
	if zeroRecorder.Body.Len() != 8192 || bytes.Count(zeroRecorder.Body.Bytes(), []byte{0}) != 8192 {
		t.Fatalf("zero payload length=%d; want 8192 zero bytes", zeroRecorder.Body.Len())
	}
	if zeroRecorder.Header().Get("Cache-Control") != "no-store, no-transform" ||
		zeroRecorder.Header().Get("X-Accel-Buffering") != "no" ||
		zeroRecorder.Header().Get("X-Netspeed-Payload") != "zero" ||
		zeroRecorder.Header().Get("X-Netspeed-Framing") != "fixed" ||
		zeroRecorder.Header().Get("X-Netspeed-Chunk-Bytes") != "4096" ||
		zeroRecorder.Header().Get("X-Netspeed-Flush") != "false" ||
		zeroRecorder.Header().Get("Content-Length") != "8192" {
		t.Fatalf("zero headers=%v", zeroRecorder.Header())
	}

	randomRequest := httptest.NewRequest(http.MethodGet, "/__down?bytes=8192&payload=random&framing=chunked&chunkBytes=4096&flush=false", nil)
	randomRecorder := httptest.NewRecorder()
	s.handleDown(randomRecorder, randomRequest)
	if randomRecorder.Code != http.StatusOK || randomRecorder.Body.Len() != 8192 {
		t.Fatalf("random status=%d length=%d", randomRecorder.Code, randomRecorder.Body.Len())
	}
	if randomRecorder.Header().Get("Content-Length") != "" || !randomRecorder.Flushed ||
		randomRecorder.Header().Get("X-Netspeed-Payload") != "random" ||
		randomRecorder.Header().Get("X-Netspeed-Framing") != "chunked" ||
		randomRecorder.Header().Get("X-Netspeed-Chunk-Bytes") != "4096" ||
		randomRecorder.Header().Get("X-Netspeed-Flush") != "false" {
		t.Fatalf("chunked headers=%v flushed=%v", randomRecorder.Header(), randomRecorder.Flushed)
	}
	body := randomRecorder.Body.Bytes()
	if bytes.Equal(body[:4096], body[4096:]) {
		t.Fatal("adjacent random chunks repeated")
	}
}

func TestHandleDownRejectsInvalidDiscriminator(t *testing.T) {
	s := measurementTestServer(1024)
	recorder := httptest.NewRecorder()
	s.handleDown(recorder, httptest.NewRequest(http.MethodGet, "/__down?payload=compressible", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d; want 400", recorder.Code)
	}
}

func TestHandlePingReturnsZeroPayloadWithMeasurementControls(t *testing.T) {
	s := measurementTestServer(1024)
	recorder := httptest.NewRecorder()
	s.handlePing(recorder, httptest.NewRequest(http.MethodGet, "/__ping?during=download&seq=1", nil))
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Length") != "0" || recorder.Header().Get("X-Netspeed-Measurement") != "latency" ||
		recorder.Header().Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("headers=%v", recorder.Header())
	}

	headRecorder := httptest.NewRecorder()
	s.handlePing(headRecorder, httptest.NewRequest(http.MethodHead, "/__ping?seq=2", nil))
	if headRecorder.Code != http.StatusOK || headRecorder.Body.Len() != 0 || headRecorder.Header().Get("Content-Length") != "0" {
		t.Fatalf("HEAD status=%d headers=%v body=%q", headRecorder.Code, headRecorder.Header(), headRecorder.Body.String())
	}
}

func TestHandleUpRejectsCompressedAndMismatchedBodies(t *testing.T) {
	s := measurementTestServer(16)

	compressed := httptest.NewRequest(http.MethodPost, "/__up?bytes=4", bytes.NewReader([]byte("1234")))
	compressed.Header.Set("Content-Encoding", "gzip")
	compressedRecorder := httptest.NewRecorder()
	s.handleUp(compressedRecorder, compressed)
	if compressedRecorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("compressed status=%d; want 415", compressedRecorder.Code)
	}

	mismatch := httptest.NewRequest(http.MethodPost, "/__up?bytes=5", bytes.NewReader([]byte("1234")))
	mismatchRecorder := httptest.NewRecorder()
	s.handleUp(mismatchRecorder, mismatch)
	if mismatchRecorder.Code != http.StatusBadRequest {
		t.Fatalf("mismatch status=%d; want 400", mismatchRecorder.Code)
	}

	streamMismatch := httptest.NewRequest(http.MethodPost, "/__up?bytes=5", bytes.NewReader([]byte("1234")))
	streamMismatch.ContentLength = -1
	streamMismatchRecorder := httptest.NewRecorder()
	s.handleUp(streamMismatchRecorder, streamMismatch)
	if streamMismatchRecorder.Code != http.StatusBadRequest || streamMismatchRecorder.Header().Get("X-Netspeed-Framing") != "chunked" {
		t.Fatalf("stream mismatch status=%d headers=%v; want 400 chunked", streamMismatchRecorder.Code, streamMismatchRecorder.Header())
	}
}

var _ io.ReadCloser = (*errorReadCloser)(nil)
