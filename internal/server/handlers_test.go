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
	"github.com/yellowman/netspeed/internal/meta"
	"github.com/yellowman/netspeed/internal/protocol"
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
	return &Server{
		cfg: &config.Config{
			MaxBytes:           maxBytes,
			EnableServerTiming: true,
		},
		metaProvider: &meta.StaticProvider{Hostname: "test", Colo: "TST"},
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
	if got.MeasurementProtocolVersion != protocol.MeasurementProtocolVersion {
		t.Fatalf("measurementProtocolVersion = %d; want %d", got.MeasurementProtocolVersion, protocol.MeasurementProtocolVersion)
	}
	if got.UploadReceiptVersion != protocol.UploadReceiptVersion {
		t.Fatalf("uploadReceiptVersion = %d; want %d", got.UploadReceiptVersion, protocol.UploadReceiptVersion)
	}
}

func TestHandleUpReturnsVerifiedReceipt(t *testing.T) {
	s := measurementTestServer(16)
	req := httptest.NewRequest(http.MethodPost, "/__up?measId=test", bytes.NewReader([]byte("1234")))
	rec := httptest.NewRecorder()

	s.handleUp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q; want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", got)
	}
	var receipt protocol.UploadReceipt
	if err := json.NewDecoder(rec.Body).Decode(&receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if !receipt.OK || receipt.AcceptedBytes != 4 || receipt.ServerDurationNS <= 0 {
		t.Fatalf("receipt = %#v; want verified 4-byte receipt", receipt)
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

var _ io.ReadCloser = (*errorReadCloser)(nil)
