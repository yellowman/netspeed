package protocol

import (
	"errors"
	"fmt"
	"io"
	"math"
)

var (
	// ErrUploadTooLarge means the request body exceeded the negotiated transfer ceiling.
	ErrUploadTooLarge = errors.New("upload exceeds maximum allowed")
	// ErrUploadLengthMismatch means a request with a known Content-Length ended early.
	ErrUploadLengthMismatch = errors.New("upload body length mismatch")
)

// ReadUpload consumes an upload body while proving that it is complete and no
// larger than maxBytes. It reads at most maxBytes+1 bytes, so an unknown-length
// request cannot be silently truncated into a successful measurement.
func ReadUpload(r io.Reader, contentLength, maxBytes int64) (int64, error) {
	if maxBytes < 0 {
		return 0, fmt.Errorf("invalid upload limit %d", maxBytes)
	}
	if contentLength > maxBytes {
		return 0, ErrUploadTooLarge
	}

	limit := maxBytes
	if maxBytes < math.MaxInt64 {
		limit++
	}

	n, err := io.Copy(io.Discard, io.LimitReader(r, limit))
	if err != nil {
		return n, fmt.Errorf("read upload body: %w", err)
	}
	if n > maxBytes {
		return n, ErrUploadTooLarge
	}
	if contentLength >= 0 && n != contentLength {
		return n, fmt.Errorf("%w: received %d bytes; expected %d", ErrUploadLengthMismatch, n, contentLength)
	}
	return n, nil
}
