package measurementhttp

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const preferenceAuto = "auto"

// Capabilities is the versioned HTTP measurement-transport contract advertised
// by /meta. Paths must remain same-origin relative paths; clients must never
// treat capability metadata as permission to send measurement traffic to a
// different authority.
type Capabilities struct {
	Version                       int      `json:"version"`
	DownloadPath                  string   `json:"downloadPath"`
	DownloadBytesParameter        string   `json:"downloadBytesParameter"`
	DownloadPayloadParameter      string   `json:"downloadPayloadParameter"`
	DownloadFramingParameter      string   `json:"downloadFramingParameter"`
	DownloadChunkBytesParameter   string   `json:"downloadChunkBytesParameter"`
	DownloadFlushParameter        string   `json:"downloadFlushParameter"`
	UploadPath                    string   `json:"uploadPath"`
	UploadBytesParameter          string   `json:"uploadBytesParameter"`
	HTTPPingPath                  string   `json:"httpPingPath"`
	HTTPPingMethods               []string `json:"httpPingMethods"`
	WebSocketPingPath             string   `json:"webSocketPingPath,omitempty"`
	WarmConnectionPing            bool     `json:"warmConnectionPing"`
	DownloadPayloads              []string `json:"downloadPayloads"`
	DownloadFramings              []string `json:"downloadFramings"`
	DefaultDownloadPayload        string   `json:"defaultDownloadPayload"`
	DefaultDownloadFraming        string   `json:"defaultDownloadFraming"`
	DefaultChunkBytes             int      `json:"defaultChunkBytes"`
	MinimumChunkBytes             int      `json:"minimumChunkBytes"`
	MaximumChunkBytes             int      `json:"maximumChunkBytes"`
	UploadContentEncodings        []string `json:"uploadContentEncodings"`
	ResponseCacheControl          string   `json:"responseCacheControl"`
	NoTransform                   bool     `json:"noTransform"`
	ProxyBufferSuppressionHeader  string   `json:"proxyBufferSuppressionHeader"`
	ProxyRequestBufferingAdvisory bool     `json:"proxyRequestBufferingAdvisory"`
}

// Preferences contains client-requested HTTP transport discriminators. Empty
// string values are treated as auto; DownloadChunkBytes=0 selects the daemon's
// advertised default.
type Preferences struct {
	DownloadPayload    string
	DownloadFraming    string
	DownloadChunkBytes int
	DownloadFlush      string
}

// Selection is the normalized transport contract used for every request in one
// test run. It is safe to expose in machine-readable results so a zero-fill run
// is never mistaken for a pseudorandom run.
type Selection struct {
	CapabilityVersion    int     `json:"capabilityVersion"`
	LegacyFallback       bool    `json:"legacyFallback"`
	DownloadPath         string  `json:"downloadPath"`
	DownloadBytesKey     string  `json:"downloadBytesParameter"`
	DownloadPayloadKey   string  `json:"downloadPayloadParameter,omitempty"`
	DownloadFramingKey   string  `json:"downloadFramingParameter,omitempty"`
	DownloadChunkKey     string  `json:"downloadChunkBytesParameter,omitempty"`
	DownloadFlushKey     string  `json:"downloadFlushParameter,omitempty"`
	DownloadPayload      Payload `json:"downloadPayload"`
	DownloadFraming      Framing `json:"downloadFraming"`
	DownloadChunkBytes   int     `json:"downloadChunkBytes"`
	DownloadFlush        bool    `json:"downloadFlush"`
	UploadPath           string  `json:"uploadPath"`
	UploadBytesKey       string  `json:"uploadBytesParameter,omitempty"`
	UploadEncoding       string  `json:"uploadContentEncoding"`
	LatencyPath          string  `json:"latencyPath"`
	LatencyMethod        string  `json:"latencyMethod"`
	LatencyUsesDownload  bool    `json:"latencyUsesDownloadEndpoint"`
	WarmConnectionPing   bool    `json:"warmConnectionPing"`
	NoTransform          bool    `json:"noTransform"`
	ResponseCacheControl string  `json:"responseCacheControl,omitempty"`
}

// LegacySelection preserves the measurement-protocol-v2 endpoint defaults for
// servers that predate the optional transport-capability object.
func LegacySelection() Selection {
	return Selection{
		LegacyFallback:      true,
		DownloadPath:        "/__down",
		DownloadBytesKey:    "bytes",
		DownloadPayload:     PayloadRandom,
		DownloadFraming:     FramingFixed,
		DownloadChunkBytes:  DefaultChunkBytes,
		UploadPath:          "/__up",
		UploadEncoding:      "identity",
		LatencyPath:         "/__down",
		LatencyMethod:       http.MethodGet,
		LatencyUsesDownload: true,
		WarmConnectionPing:  false,
	}
}

// Negotiate validates an advertised transport contract and selects the exact
// discriminators requested by the client. Explicit controls are not silently
// sent to legacy servers that did not advertise support for them.
func Negotiate(capabilities *Capabilities, preferences Preferences) (Selection, error) {
	normalized, explicit, err := normalizePreferences(preferences)
	if err != nil {
		return Selection{}, err
	}
	if capabilities == nil {
		if explicit {
			return Selection{}, fmt.Errorf("server does not advertise measurementCapabilities; explicit HTTP transport controls require transport version %d", TransportVersion)
		}
		return LegacySelection(), nil
	}
	if err := ValidateCapabilities(capabilities); err != nil {
		return Selection{}, err
	}

	payload := Payload(normalized.DownloadPayload)
	if normalized.DownloadPayload == preferenceAuto {
		payload = Payload(strings.ToLower(strings.TrimSpace(capabilities.DefaultDownloadPayload)))
	}
	if !containsFold(capabilities.DownloadPayloads, string(payload)) {
		return Selection{}, fmt.Errorf("server does not support download payload %q", payload)
	}

	framing := Framing(normalized.DownloadFraming)
	if normalized.DownloadFraming == preferenceAuto {
		framing = Framing(strings.ToLower(strings.TrimSpace(capabilities.DefaultDownloadFraming)))
	}
	if !containsFold(capabilities.DownloadFramings, string(framing)) {
		return Selection{}, fmt.Errorf("server does not support download framing %q", framing)
	}

	chunkBytes := normalized.DownloadChunkBytes
	if chunkBytes == 0 {
		chunkBytes = capabilities.DefaultChunkBytes
	}
	if chunkBytes < capabilities.MinimumChunkBytes || chunkBytes > capabilities.MaximumChunkBytes {
		return Selection{}, fmt.Errorf("download chunk size %d is outside server range %d..%d", chunkBytes, capabilities.MinimumChunkBytes, capabilities.MaximumChunkBytes)
	}

	flush := framing == FramingChunked
	if normalized.DownloadFlush != preferenceAuto {
		flush, err = strconv.ParseBool(normalized.DownloadFlush)
		if err != nil {
			return Selection{}, fmt.Errorf("invalid download flush preference %q", normalized.DownloadFlush)
		}
	}

	latencyPath := capabilities.HTTPPingPath
	latencyMethod := preferredPingMethod(capabilities.HTTPPingMethods)
	latencyUsesDownload := false
	if latencyPath == "" {
		latencyPath = capabilities.DownloadPath
		latencyMethod = http.MethodGet
		latencyUsesDownload = true
	}

	return Selection{
		CapabilityVersion:    capabilities.Version,
		DownloadPath:         capabilities.DownloadPath,
		DownloadBytesKey:     capabilities.DownloadBytesParameter,
		DownloadPayloadKey:   capabilities.DownloadPayloadParameter,
		DownloadFramingKey:   capabilities.DownloadFramingParameter,
		DownloadChunkKey:     capabilities.DownloadChunkBytesParameter,
		DownloadFlushKey:     capabilities.DownloadFlushParameter,
		DownloadPayload:      payload,
		DownloadFraming:      framing,
		DownloadChunkBytes:   chunkBytes,
		DownloadFlush:        flush,
		UploadPath:           capabilities.UploadPath,
		UploadBytesKey:       capabilities.UploadBytesParameter,
		UploadEncoding:       "identity",
		LatencyPath:          latencyPath,
		LatencyMethod:        latencyMethod,
		LatencyUsesDownload:  latencyUsesDownload,
		WarmConnectionPing:   capabilities.WarmConnectionPing,
		NoTransform:          capabilities.NoTransform,
		ResponseCacheControl: capabilities.ResponseCacheControl,
	}, nil
}

// ValidateCapabilities rejects incomplete, contradictory, or unsafe metadata.
// In particular, endpoint values cannot contain an authority, query, fragment,
// path traversal, or scheme-relative form.
func ValidateCapabilities(capabilities *Capabilities) error {
	if capabilities == nil {
		return fmt.Errorf("measurement capabilities are missing")
	}
	if capabilities.Version < TransportVersion {
		return fmt.Errorf("server HTTP transport capability version %d is too old; need %d", capabilities.Version, TransportVersion)
	}
	for name, value := range map[string]string{
		"downloadPath": capabilities.DownloadPath,
		"uploadPath":   capabilities.UploadPath,
	} {
		if err := validateEndpointPath(name, value, true); err != nil {
			return err
		}
	}
	if err := validateEndpointPath("httpPingPath", capabilities.HTTPPingPath, false); err != nil {
		return err
	}
	if err := validateEndpointPath("webSocketPingPath", capabilities.WebSocketPingPath, false); err != nil {
		return err
	}
	downloadParameters := map[string]string{
		"downloadBytesParameter":      capabilities.DownloadBytesParameter,
		"downloadPayloadParameter":    capabilities.DownloadPayloadParameter,
		"downloadFramingParameter":    capabilities.DownloadFramingParameter,
		"downloadChunkBytesParameter": capabilities.DownloadChunkBytesParameter,
		"downloadFlushParameter":      capabilities.DownloadFlushParameter,
	}
	for name, value := range downloadParameters {
		if err := validateParameterName(name, value); err != nil {
			return err
		}
	}
	if err := validateDistinctParameterNames(downloadParameters); err != nil {
		return err
	}
	if err := validateParameterName("uploadBytesParameter", capabilities.UploadBytesParameter); err != nil {
		return err
	}

	defaultPayload := strings.ToLower(strings.TrimSpace(capabilities.DefaultDownloadPayload))
	if defaultPayload != string(PayloadRandom) && defaultPayload != string(PayloadZero) {
		return fmt.Errorf("invalid default download payload %q", capabilities.DefaultDownloadPayload)
	}
	if !containsFold(capabilities.DownloadPayloads, defaultPayload) {
		return fmt.Errorf("default download payload %q is not advertised as supported", defaultPayload)
	}
	if !containsFold(capabilities.DownloadPayloads, string(PayloadRandom)) {
		return fmt.Errorf("transport version %d must support pseudorandom downloads", TransportVersion)
	}

	defaultFraming := strings.ToLower(strings.TrimSpace(capabilities.DefaultDownloadFraming))
	if defaultFraming != string(FramingFixed) && defaultFraming != string(FramingChunked) {
		return fmt.Errorf("invalid default download framing %q", capabilities.DefaultDownloadFraming)
	}
	if !containsFold(capabilities.DownloadFramings, defaultFraming) {
		return fmt.Errorf("default download framing %q is not advertised as supported", defaultFraming)
	}
	if !containsFold(capabilities.DownloadFramings, string(FramingFixed)) {
		return fmt.Errorf("transport version %d must support fixed framing", TransportVersion)
	}

	if capabilities.MinimumChunkBytes <= 0 || capabilities.MaximumChunkBytes < capabilities.MinimumChunkBytes {
		return fmt.Errorf("invalid advertised download chunk range %d..%d", capabilities.MinimumChunkBytes, capabilities.MaximumChunkBytes)
	}
	if capabilities.DefaultChunkBytes < capabilities.MinimumChunkBytes || capabilities.DefaultChunkBytes > capabilities.MaximumChunkBytes {
		return fmt.Errorf("default download chunk size %d is outside advertised range %d..%d", capabilities.DefaultChunkBytes, capabilities.MinimumChunkBytes, capabilities.MaximumChunkBytes)
	}
	if !containsFold(capabilities.UploadContentEncodings, "identity") {
		return fmt.Errorf("server does not advertise identity upload content encoding")
	}
	if !capabilities.NoTransform || !hasDirective(capabilities.ResponseCacheControl, "no-transform") || !hasDirective(capabilities.ResponseCacheControl, "no-store") {
		return fmt.Errorf("server transport capabilities do not guarantee no-store, no-transform responses")
	}

	if capabilities.HTTPPingPath != "" {
		if preferredPingMethod(capabilities.HTTPPingMethods) == "" {
			return fmt.Errorf("httpPingPath is advertised without a supported GET or HEAD method")
		}
	}
	return nil
}

func normalizePreferences(preferences Preferences) (Preferences, bool, error) {
	normalized := preferences
	normalized.DownloadPayload = normalizePreference(preferences.DownloadPayload)
	normalized.DownloadFraming = normalizePreference(preferences.DownloadFraming)
	normalized.DownloadFlush = normalizePreference(preferences.DownloadFlush)

	switch normalized.DownloadPayload {
	case preferenceAuto, string(PayloadRandom), string(PayloadZero):
	default:
		return Preferences{}, false, fmt.Errorf("download payload must be auto, random, or zero")
	}
	switch normalized.DownloadFraming {
	case preferenceAuto, string(FramingFixed), string(FramingChunked):
	default:
		return Preferences{}, false, fmt.Errorf("download framing must be auto, fixed, or chunked")
	}
	switch normalized.DownloadFlush {
	case preferenceAuto, "true", "false":
	default:
		return Preferences{}, false, fmt.Errorf("download flush must be auto, true, or false")
	}
	if normalized.DownloadChunkBytes < 0 {
		return Preferences{}, false, fmt.Errorf("download chunk bytes cannot be negative")
	}
	explicit := normalized.DownloadPayload != preferenceAuto ||
		normalized.DownloadFraming != preferenceAuto ||
		normalized.DownloadChunkBytes != 0 ||
		normalized.DownloadFlush != preferenceAuto
	return normalized, explicit, nil
}

func normalizePreference(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return preferenceAuto
	}
	return value
}

func validateEndpointPath(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if strings.TrimSpace(value) != value || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return fmt.Errorf("unsafe %s %q", name, value)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || strings.Contains(parsed.Path, "\\") {
		return fmt.Errorf("unsafe %s %q", name, value)
	}
	if cleaned := path.Clean(parsed.Path); cleaned != parsed.Path || cleaned == "." {
		return fmt.Errorf("unclean %s %q", name, value)
	}
	return nil
}

func validateDistinctParameterNames(parameters map[string]string) error {
	seen := make(map[string]string, len(parameters))
	for field, value := range parameters {
		if previous, exists := seen[value]; exists {
			return fmt.Errorf("%s and %s must not use the same query parameter %q", previous, field, value)
		}
		seen[value] = field
	}
	return nil
}

func validateParameterName(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	for index, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
		if !valid || index == 0 && r >= '0' && r <= '9' {
			return fmt.Errorf("unsafe %s %q", name, value)
		}
	}
	return nil
}

func preferredPingMethod(methods []string) string {
	if containsFold(methods, http.MethodGet) {
		return http.MethodGet
	}
	if containsFold(methods, http.MethodHead) {
		return http.MethodHead
	}
	return ""
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func hasDirective(headerValue, target string) bool {
	for _, value := range strings.Split(headerValue, ",") {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
