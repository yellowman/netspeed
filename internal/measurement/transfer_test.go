package measurement

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadUploadBodyAcceptsExactLimit(t *testing.T) {
	n, err := ReadUploadBody(strings.NewReader("abcd"), 4)
	if err != nil {
		t.Fatalf("ReadUploadBody returned error: %v", err)
	}
	if n != 4 {
		t.Fatalf("ReadUploadBody read %d bytes, want 4", n)
	}
}

func TestReadUploadBodyRejectsLimitPlusOne(t *testing.T) {
	n, err := ReadUploadBody(strings.NewReader("abcde"), 4)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("ReadUploadBody error = %v, want ErrBodyTooLarge", err)
	}
	if n != 5 {
		t.Fatalf("ReadUploadBody read %d bytes, want 5-byte lookahead", n)
	}
}

func TestReadUploadBodyPropagatesReadFailure(t *testing.T) {
	boom := errors.New("boom")
	n, err := ReadUploadBody(errorReader{err: boom}, 4)
	if !errors.Is(err, boom) {
		t.Fatalf("ReadUploadBody error = %v, want wrapped boom", err)
	}
	if n != 0 {
		t.Fatalf("ReadUploadBody read %d bytes, want 0", n)
	}
}

func TestDecodeUploadReceipt(t *testing.T) {
	receipt, err := DecodeUploadReceipt(strings.NewReader(
		`{"ok":true,"acceptedBytes":1234,"serverDurationNs":5678}`,
	))
	if err != nil {
		t.Fatalf("DecodeUploadReceipt returned error: %v", err)
	}
	if receipt.AcceptedBytes != 1234 || receipt.ServerDurationNS != 5678 {
		t.Fatalf("DecodeUploadReceipt = %#v", receipt)
	}
}

func TestDecodeUploadReceiptRejectsInvalidContractFields(t *testing.T) {
	cases := []string{
		`{"ok":false,"acceptedBytes":1,"serverDurationNs":1}`,
		`{"ok":true,"acceptedBytes":-1,"serverDurationNs":1}`,
		`{"ok":true,"acceptedBytes":1,"serverDurationNs":0}`,
	}
	for _, body := range cases {
		if _, err := DecodeUploadReceipt(strings.NewReader(body)); err == nil {
			t.Fatalf("DecodeUploadReceipt(%q) unexpectedly succeeded", body)
		}
	}
}

func TestDecodeUploadReceiptRejectsOversizeResponse(t *testing.T) {
	body := strings.Repeat(" ", int(MaxReceiptBytes)+1)
	if _, err := DecodeUploadReceipt(strings.NewReader(body)); err == nil {
		t.Fatal("DecodeUploadReceipt unexpectedly accepted an oversized response")
	}
}

func TestDecodeUploadReceiptRejectsUnknownOrTrailingData(t *testing.T) {
	cases := []string{
		`{"ok":true,"acceptedBytes":1,"serverDurationNs":1,"extra":true}`,
		`{"ok":true,"acceptedBytes":1,"serverDurationNs":1}{}`,
	}
	for _, body := range cases {
		if _, err := DecodeUploadReceipt(strings.NewReader(body)); err == nil {
			t.Fatalf("DecodeUploadReceipt(%q) unexpectedly succeeded", body)
		}
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

var _ io.Reader = errorReader{}
