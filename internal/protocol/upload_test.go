package protocol

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type failIfRead struct{}

func (failIfRead) Read([]byte) (int, error) {
	panic("oversized known-length body should be rejected before reading")
}

type readError struct {
	data []byte
	err  error
}

func (r *readError) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

func TestReadUploadAcceptsExactKnownLength(t *testing.T) {
	body := []byte("verified")
	n, err := ReadUpload(bytes.NewReader(body), int64(len(body)), int64(len(body)))
	if err != nil {
		t.Fatalf("ReadUpload returned error: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("ReadUpload consumed %d bytes; want %d", n, len(body))
	}
}

func TestReadUploadAcceptsExactUnknownLength(t *testing.T) {
	body := []byte("verified")
	n, err := ReadUpload(bytes.NewReader(body), -1, int64(len(body)))
	if err != nil {
		t.Fatalf("ReadUpload returned error: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("ReadUpload consumed %d bytes; want %d", n, len(body))
	}
}

func TestReadUploadRejectsKnownOversizeWithoutReading(t *testing.T) {
	n, err := ReadUpload(failIfRead{}, 11, 10)
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("ReadUpload error = %v; want ErrUploadTooLarge", err)
	}
	if n != 0 {
		t.Fatalf("ReadUpload consumed %d bytes; want 0", n)
	}
}

func TestReadUploadRejectsUnknownOversize(t *testing.T) {
	n, err := ReadUpload(bytes.NewReader([]byte("12345")), -1, 4)
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("ReadUpload error = %v; want ErrUploadTooLarge", err)
	}
	if n != 5 {
		t.Fatalf("ReadUpload consumed %d bytes; want the 5-byte detection read", n)
	}
}

func TestReadUploadRejectsTruncatedKnownLength(t *testing.T) {
	n, err := ReadUpload(bytes.NewReader([]byte("123")), 5, 10)
	if !errors.Is(err, ErrUploadLengthMismatch) {
		t.Fatalf("ReadUpload error = %v; want ErrUploadLengthMismatch", err)
	}
	if n != 3 {
		t.Fatalf("ReadUpload consumed %d bytes; want 3", n)
	}
}

func TestReadUploadPreservesReadErrors(t *testing.T) {
	boom := errors.New("boom")
	n, err := ReadUpload(&readError{data: []byte("123"), err: boom}, -1, 10)
	if !errors.Is(err, boom) {
		t.Fatalf("ReadUpload error = %v; want wrapped read error", err)
	}
	if n != 3 {
		t.Fatalf("ReadUpload consumed %d bytes; want 3", n)
	}
}

func TestReadUploadRejectsNegativeLimit(t *testing.T) {
	_, err := ReadUpload(bytes.NewReader(nil), 0, -1)
	if err == nil {
		t.Fatal("ReadUpload accepted a negative limit")
	}
}

func TestZeroLengthUpload(t *testing.T) {
	n, err := ReadUpload(bytes.NewReader(nil), 0, 0)
	if err != nil {
		t.Fatalf("ReadUpload returned error: %v", err)
	}
	if n != 0 {
		t.Fatalf("ReadUpload consumed %d bytes; want 0", n)
	}
}

var _ io.Reader = (*readError)(nil)
