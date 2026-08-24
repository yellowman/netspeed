// Package measurement contains the protocol primitives shared by netspeedd and
// its clients. It intentionally depends only on the Go standard library so the
// transfer contract can be tested in isolation.
package measurement

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	// APIVersion is advertised by /meta when the server supports byte-counted
	// upload receipts and publishes its transfer ceiling.
	APIVersion = 1

	// MaxReceiptBytes bounds the small JSON response returned by /__up.
	MaxReceiptBytes int64 = 64 << 10
)

// ErrBodyTooLarge indicates that an upload body exceeded the configured limit.
var ErrBodyTooLarge = errors.New("upload body exceeds maximum allowed size")

// UploadReceipt is returned by POST /__up after the complete request body has
// been read successfully.
type UploadReceipt struct {
	OK               bool  `json:"ok"`
	AcceptedBytes    int64 `json:"acceptedBytes"`
	ServerDurationNS int64 `json:"serverDurationNs"`
}

// ReadUploadBody consumes an upload body while retaining one byte of lookahead
// so a body larger than maxBytes can never be mistaken for a successful upload.
// The returned count includes that lookahead byte when ErrBodyTooLarge is
// returned.
func ReadUploadBody(r io.Reader, maxBytes int64) (int64, error) {
	if r == nil {
		return 0, errors.New("upload body is nil")
	}
	if maxBytes < 0 {
		return 0, errors.New("maximum upload size cannot be negative")
	}

	limit := maxBytes
	canDetectOverflow := maxBytes < math.MaxInt64
	if canDetectOverflow {
		limit++
	}

	n, err := io.Copy(io.Discard, io.LimitReader(r, limit))
	if err != nil {
		return n, fmt.Errorf("read upload body: %w", err)
	}
	if canDetectOverflow && n > maxBytes {
		return n, ErrBodyTooLarge
	}
	return n, nil
}

// DecodeUploadReceipt decodes and validates the bounded JSON response emitted
// by a measurement API v1 server.
func DecodeUploadReceipt(r io.Reader) (UploadReceipt, error) {
	if r == nil {
		return UploadReceipt{}, errors.New("upload receipt body is nil")
	}

	data, err := io.ReadAll(io.LimitReader(r, MaxReceiptBytes+1))
	if err != nil {
		return UploadReceipt{}, fmt.Errorf("read upload receipt: %w", err)
	}
	if int64(len(data)) > MaxReceiptBytes {
		return UploadReceipt{}, fmt.Errorf("upload receipt exceeds %d bytes", MaxReceiptBytes)
	}

	var receipt UploadReceipt
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&receipt); err != nil {
		return UploadReceipt{}, fmt.Errorf("decode upload receipt: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return UploadReceipt{}, err
	}
	if !receipt.OK {
		return UploadReceipt{}, errors.New("upload receipt did not confirm success")
	}
	if receipt.AcceptedBytes < 0 {
		return UploadReceipt{}, errors.New("upload receipt contains a negative byte count")
	}
	if receipt.ServerDurationNS <= 0 {
		return UploadReceipt{}, errors.New("upload receipt contains a non-positive duration")
	}
	return receipt, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode upload receipt trailer: %w", err)
	}
	return errors.New("upload receipt contains multiple JSON values")
}
