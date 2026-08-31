package measurementhttp

import (
	"net/http"
	"strings"
	"testing"
)

func testCapabilities() *Capabilities {
	return &Capabilities{
		Version:                       TransportVersion,
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
		ResponseCacheControl:          CacheControl,
		NoTransform:                   true,
		ProxyBufferSuppressionHeader:  "X-Accel-Buffering: no",
		ProxyRequestBufferingAdvisory: true,
	}
}

func TestNegotiateDefaultsAndExplicitControls(t *testing.T) {
	defaults, err := Negotiate(testCapabilities(), Preferences{})
	if err != nil {
		t.Fatalf("Negotiate defaults: %v", err)
	}
	if defaults.LegacyFallback || defaults.DownloadPath != "/measure/down" || defaults.DownloadPayload != PayloadRandom ||
		defaults.DownloadFraming != FramingFixed || defaults.DownloadChunkBytes != 64<<10 || defaults.DownloadFlush ||
		defaults.LatencyPath != "/measure/ping" || defaults.LatencyMethod != http.MethodGet || !defaults.WarmConnectionPing {
		t.Fatalf("unexpected default selection: %#v", defaults)
	}

	explicit, err := Negotiate(testCapabilities(), Preferences{
		DownloadPayload:    "zero",
		DownloadFraming:    "chunked",
		DownloadChunkBytes: 4096,
		DownloadFlush:      "false",
	})
	if err != nil {
		t.Fatalf("Negotiate explicit controls: %v", err)
	}
	if explicit.DownloadPayload != PayloadZero || explicit.DownloadFraming != FramingChunked ||
		explicit.DownloadChunkBytes != 4096 || explicit.DownloadFlush {
		t.Fatalf("unexpected explicit selection: %#v", explicit)
	}
}

func TestNegotiateChunkedAutoFlushes(t *testing.T) {
	selection, err := Negotiate(testCapabilities(), Preferences{DownloadFraming: "chunked"})
	if err != nil {
		t.Fatalf("Negotiate chunked: %v", err)
	}
	if !selection.DownloadFlush {
		t.Fatal("chunked auto selection did not enable per-chunk flushing")
	}
}

func TestNegotiateLegacyOnlyWhenControlsAreAutomatic(t *testing.T) {
	selection, err := Negotiate(nil, Preferences{})
	if err != nil {
		t.Fatalf("legacy auto negotiation: %v", err)
	}
	if !selection.LegacyFallback || selection.LatencyPath != "/__down" || !selection.LatencyUsesDownload {
		t.Fatalf("unexpected legacy selection: %#v", selection)
	}

	_, err = Negotiate(nil, Preferences{DownloadPayload: "zero"})
	if err == nil || !strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("explicit legacy negotiation error = %v", err)
	}
}

func TestNegotiateRejectsUnsupportedAndOutOfRangeControls(t *testing.T) {
	for _, preferences := range []Preferences{
		{DownloadPayload: "text"},
		{DownloadFraming: "buffered"},
		{DownloadChunkBytes: 1},
		{DownloadFlush: "sometimes"},
	} {
		if _, err := Negotiate(testCapabilities(), preferences); err == nil {
			t.Fatalf("Negotiate accepted invalid preferences: %#v", preferences)
		}
	}

	capabilities := testCapabilities()
	capabilities.DownloadPayloads = []string{"random"}
	if _, err := Negotiate(capabilities, Preferences{DownloadPayload: "zero"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported payload error = %v", err)
	}
}

func TestValidateCapabilitiesRejectsUnsafeMetadata(t *testing.T) {
	mutations := []func(*Capabilities){
		func(c *Capabilities) { c.DownloadPath = "https://attacker.example/down" },
		func(c *Capabilities) { c.DownloadPath = "//attacker.example/down" },
		func(c *Capabilities) { c.DownloadPath = "/measure/../down" },
		func(c *Capabilities) { c.DownloadBytesParameter = "n&redirect" },
		func(c *Capabilities) { c.ResponseCacheControl = "no-store" },
		func(c *Capabilities) { c.NoTransform = false },
		func(c *Capabilities) { c.UploadContentEncodings = []string{"gzip"} },
		func(c *Capabilities) { c.HTTPPingMethods = []string{"POST"} },
	}
	for index, mutate := range mutations {
		capabilities := testCapabilities()
		mutate(capabilities)
		if err := ValidateCapabilities(capabilities); err == nil {
			t.Fatalf("mutation %d was accepted: %#v", index, capabilities)
		}
	}
}

func TestNegotiateFallsBackToZeroByteDownloadWhenPingPathMissing(t *testing.T) {
	capabilities := testCapabilities()
	capabilities.HTTPPingPath = ""
	capabilities.HTTPPingMethods = nil
	selection, err := Negotiate(capabilities, Preferences{})
	if err != nil {
		t.Fatalf("Negotiate fallback ping: %v", err)
	}
	if selection.LatencyPath != capabilities.DownloadPath || selection.LatencyMethod != http.MethodGet || !selection.LatencyUsesDownload {
		t.Fatalf("unexpected ping fallback: %#v", selection)
	}
}

func TestValidateCapabilitiesRejectsDuplicateParameterNames(t *testing.T) {
	capabilities := testCapabilities()
	capabilities.DownloadPayloadParameter = capabilities.DownloadBytesParameter
	if err := ValidateCapabilities(capabilities); err == nil || !strings.Contains(err.Error(), "same query parameter") {
		t.Fatalf("ValidateCapabilities error = %v; want duplicate-parameter rejection", err)
	}
}

func TestValidateCapabilitiesRejectsEncodedBackslashPath(t *testing.T) {
	capabilities := testCapabilities()
	capabilities.DownloadPath = `/measure%5c..%5cexfiltrate`
	if err := ValidateCapabilities(capabilities); err == nil || !strings.Contains(err.Error(), "unsafe downloadPath") {
		t.Fatalf("ValidateCapabilities error = %v; want encoded-backslash rejection", err)
	}
}
