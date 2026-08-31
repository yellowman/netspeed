package measurementhttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDownloadDefaultsAndAliases(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/__down?bytes=123&fill=zero&stream=chunked&chunkBytes=4096", nil)
	options, err := ParseDownload(request, 1024)
	if err != nil {
		t.Fatalf("ParseDownload: %v", err)
	}
	if options.Bytes != 123 || options.Payload != PayloadZero || options.Framing != FramingChunked || options.ChunkBytes != 4096 || !options.Flush {
		t.Fatalf("unexpected options: %+v", options)
	}

	defaults, err := ParseDownload(httptest.NewRequest(http.MethodGet, "/__down", nil), 1024)
	if err != nil {
		t.Fatalf("ParseDownload defaults: %v", err)
	}
	if defaults.Bytes != 0 || defaults.Payload != PayloadRandom || defaults.Framing != FramingFixed || defaults.ChunkBytes != DefaultChunkBytes || defaults.Flush {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
}

func TestParseDownloadRejectsConflictsAndUnsafeChunkSizes(t *testing.T) {
	for _, target := range []string{
		"/__down?payload=random&fill=zero",
		"/__down?framing=fixed&stream=chunked",
		"/__down?chunkBytes=1",
		"/__down?flush=maybe",
		"/__down?bytes=1025",
	} {
		_, err := ParseDownload(httptest.NewRequest(http.MethodGet, target, nil), 1024)
		if err == nil || StatusCode(err, 0) != http.StatusBadRequest {
			t.Fatalf("ParseDownload(%q) error=%v status=%d; want 400", target, err, StatusCode(err, 0))
		}
	}
}

func TestParseUploadExpectedBytesAndEncoding(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/__up?bytes=42", nil)
	expected, present, err := ParseUploadExpectedBytes(request, 100)
	if err != nil || !present || expected != 42 {
		t.Fatalf("ParseUploadExpectedBytes = %d, %v, %v", expected, present, err)
	}
	request.Header.Set("Content-Encoding", "identity")
	if err := ValidateIdentityContentEncoding(request); err != nil {
		t.Fatalf("identity encoding rejected: %v", err)
	}
	request.Header.Set("Content-Encoding", "gzip")
	if err := ValidateIdentityContentEncoding(request); err == nil || StatusCode(err, 0) != http.StatusUnsupportedMediaType {
		t.Fatalf("gzip error=%v status=%d; want 415", err, StatusCode(err, 0))
	}
}

func TestMeasurementHeadersAndFraming(t *testing.T) {
	header := make(http.Header)
	SetResponseHeaders(header, "download")
	SetDownloadHeaders(header, DownloadOptions{Bytes: 10, Payload: PayloadZero, Framing: FramingFixed, ChunkBytes: 4096})
	if header.Get("Cache-Control") != CacheControl || header.Get("X-Accel-Buffering") != "no" ||
		header.Get("CDN-Cache-Control") != "no-store" || header.Get("Surrogate-Control") != "no-store" ||
		header.Get("Content-Length") != "10" {
		t.Fatalf("unexpected fixed headers: %v", header)
	}
	SetDownloadHeaders(header, DownloadOptions{Bytes: 10, Payload: PayloadRandom, Framing: FramingChunked, ChunkBytes: 4096})
	if header.Get("Content-Length") != "" || header.Get("X-Netspeed-Framing") != "chunked" {
		t.Fatalf("unexpected chunked headers: %v", header)
	}
}

func TestStreamZeroAndNonRepeatingRandom(t *testing.T) {
	zero := httptest.NewRecorder()
	zeroOptions := DownloadOptions{Bytes: 8192, Payload: PayloadZero, Framing: FramingFixed, ChunkBytes: 4096}
	written, err := Stream(zero, zeroOptions)
	if err != nil || written != zeroOptions.Bytes {
		t.Fatalf("zero Stream written=%d err=%v", written, err)
	}
	if bytes.Count(zero.Body.Bytes(), []byte{0}) != int(zeroOptions.Bytes) {
		t.Fatal("zero payload contained non-zero bytes")
	}

	random := httptest.NewRecorder()
	randomOptions := DownloadOptions{Bytes: 8192, Payload: PayloadRandom, Framing: FramingChunked, ChunkBytes: 4096, Flush: true}
	written, err = Stream(random, randomOptions)
	if err != nil || written != randomOptions.Bytes {
		t.Fatalf("random Stream written=%d err=%v", written, err)
	}
	body := random.Body.Bytes()
	if bytes.Equal(body, make([]byte, len(body))) {
		t.Fatal("random payload was all zero")
	}
	if bytes.Equal(body[:4096], body[4096:]) {
		t.Fatal("adjacent pseudorandom chunks repeated")
	}
	if !random.Flushed {
		t.Fatal("chunked stream did not flush")
	}
}

func TestValidateIdentityContentEncodingRejectsMalformedList(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/__up", strings.NewReader("x"))
	request.Header["Content-Encoding"] = []string{"identity, "}
	if err := ValidateIdentityContentEncoding(request); err == nil {
		t.Fatal("malformed Content-Encoding accepted")
	}
}

func TestStreamRejectsInvalidInternalOptions(t *testing.T) {
	for _, options := range []DownloadOptions{
		{Bytes: 1, Payload: "stale", Framing: FramingFixed, ChunkBytes: 4096},
		{Bytes: 1, Payload: PayloadRandom, Framing: "buffered", ChunkBytes: 4096},
		{Bytes: -1, Payload: PayloadRandom, Framing: FramingFixed, ChunkBytes: 4096},
	} {
		if _, err := Stream(httptest.NewRecorder(), options); err == nil {
			t.Fatalf("Stream accepted invalid options: %+v", options)
		}
	}
}
