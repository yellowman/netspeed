package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yellowman/netspeed/internal/config"
	"github.com/yellowman/netspeed/internal/measurement"
	"github.com/yellowman/netspeed/internal/meta"
)

func phase1TestServer(maxBytes int64) *Server {
	return &Server{
		cfg: &config.Config{
			MaxBytes:           maxBytes,
			EnableServerTiming: true,
		},
		metaProvider: &meta.StaticProvider{Hostname: "test", Colo: "TST"},
		payloadBuf:   []byte("abcd"),
	}
}

func TestHandleMetaAdvertisesMeasurementCapabilities(t *testing.T) {
	s := phase1TestServer(1234)
	recorder := httptest.NewRecorder()
	s.handleMeta(recorder, httptest.NewRequest(http.MethodGet, "/meta", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var got meta.ClientMeta
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.MeasurementAPIVersion != measurement.APIVersion || got.MaxTransferBytes != 1234 {
		t.Fatalf("capabilities = version:%d max:%d", got.MeasurementAPIVersion, got.MaxTransferBytes)
	}
}

func TestHandleUpReturnsExactReceipt(t *testing.T) {
	s := phase1TestServer(4)
	recorder := httptest.NewRecorder()
	s.handleUp(recorder, httptest.NewRequest(http.MethodPost, "/__up", strings.NewReader("abcd")))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	receipt, err := measurement.DecodeUploadReceipt(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.AcceptedBytes != 4 {
		t.Fatalf("acceptedBytes = %d, want 4", receipt.AcceptedBytes)
	}
	if receipt.ServerDurationNS <= 0 {
		t.Fatalf("serverDurationNs = %d, want positive", receipt.ServerDurationNS)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Server-Timing"); !strings.HasPrefix(got, "app;dur=") {
		t.Fatalf("Server-Timing = %q", got)
	}
}

func TestHandleUpRejectsKnownOversizeBody(t *testing.T) {
	s := phase1TestServer(4)
	recorder := httptest.NewRecorder()
	s.handleUp(recorder, httptest.NewRequest(http.MethodPost, "/__up", strings.NewReader("abcde")))

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandleUpRejectsChunkedOversizeBody(t *testing.T) {
	s := phase1TestServer(4)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/__up", strings.NewReader("abcde"))
	req.ContentLength = -1
	s.handleUp(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

func TestHandleUpRejectsReadFailure(t *testing.T) {
	s := phase1TestServer(4)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/__up", failingReadCloser{err: errors.New("boom")})
	req.ContentLength = -1
	s.handleUp(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestHandleDownReturnsExactBytesAndRejectsOversize(t *testing.T) {
	s := phase1TestServer(10)

	recorder := httptest.NewRecorder()
	s.handleDown(recorder, httptest.NewRequest(http.MethodGet, "/__down?bytes=10", nil))
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 10 {
		t.Fatalf("status = %d bytes = %d", recorder.Code, recorder.Body.Len())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}

	recorder = httptest.NewRecorder()
	s.handleDown(recorder, httptest.NewRequest(http.MethodGet, "/__down?bytes=11", nil))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d, want 413", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("oversize Cache-Control = %q", got)
	}
}

type failingReadCloser struct {
	err error
}

func (r failingReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (failingReadCloser) Close() error               { return nil }

var _ io.ReadCloser = failingReadCloser{}
