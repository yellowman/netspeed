// Package config provides configuration structures and loading for netspeedd.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxTransferBytes          = int64(1 << 30) // 1 GiB
	defaultClientBandwidthQuotaBytes = int64(1 << 40) // 1 TiB per window
	defaultOfferBodyBytes            = int64(128 << 10)
	defaultReportBodyBytes           = int64(16 << 10)
	minimumSecretLength              = 16
)

// Config holds all configuration for the netspeedd server.
type Config struct {
	// ListenAddr is the address to listen on (for example, ":8080").
	ListenAddr string

	// TLS configuration. Both files must be configured together; otherwise the
	// daemon refuses to start instead of silently falling back to cleartext HTTP.
	TLSCertFile string
	TLSKeyFile  string

	// MaxBytes is the hard cap for the /__down bytes parameter and /__up body.
	MaxBytes int64

	// HTTP timeout policy. Net/http's whole-request ReadTimeout and WriteTimeout
	// are intentionally disabled: they make slow, valid measurement streams fail
	// based on total request lifetime. ReadHeaderTimeout protects request headers,
	// while endpoint wrappers apply ControlTimeout or TransferTimeout and clear
	// connection deadlines before a keep-alive connection is reused.
	ReadHeaderTimeout time.Duration
	ControlTimeout    time.Duration
	TransferTimeout   time.Duration
	IdleTimeout       time.Duration

	// EnableServerTiming adds Server-Timing headers to responses.
	EnableServerTiming bool

	// CORS configuration. Credentials require explicit origins; wildcard origins
	// are rejected when CORSAllowCredentials is true.
	EnableCORS           bool
	AllowedOrigins       []string
	CORSAllowCredentials bool

	// LocationsFile is the path to a JSON file containing Location entries.
	LocationsFile string

	// GeoIP database paths. Either database can be used independently. The ASN
	// database supplies network ownership; the City database supplies country,
	// subdivision, city, postal code, coordinates, and timezone.
	GeoIPASNDatabasePath  string
	GeoIPCityDatabasePath string

	// Forwarding headers are honored only when the direct peer is inside one of
	// TrustedProxyCIDRs. TrustProxyHeaders is retained as an explicit guard for
	// compatibility with earlier configuration surfaces.
	TrustProxyHeaders bool
	TrustedProxyCIDRs []string

	// Hostname and server location returned by /meta.
	Hostname string
	Colo     string

	// Service admission and quota controls.
	MaxConcurrentTransfers          int
	MaxConcurrentTransfersPerClient int
	ClientBandwidthQuotaBytes       int64
	ClientBandwidthQuotaWindow      time.Duration
	MaxWebRTCSessions               int
	MaxWebRTCSessionsPerClient      int
	WebRTCOfferRatePerMinute        int
	WebRTCOfferBurst                int
	TurnCredentialRatePerMinute     int
	TurnCredentialBurst             int
	MaxOfferBodyBytes               int64
	MaxReportBodyBytes              int64

	// Optional shared-token authentication. When AccessToken is non-empty, all
	// measurement and control endpoints require Authorization: Bearer <token>.
	// /health remains public. MetricsToken can protect /metrics independently.
	AccessToken   string
	MetricsToken  string
	EnableMetrics bool

	// TURN server configuration.
	TurnSecret  string
	TurnServers []string
	TurnRealm   string
	MaxTurnTTL  int64

	// EmbeddedTurn enables the built-in TURN server. It is opt-in and defaults
	// to loopback-only. Non-loopback listeners require an explicit public IP.
	EmbeddedTurn         bool
	EmbeddedTurnAddr     string
	EmbeddedTurnPublicIP string
	EmbeddedTurnPort     string
	EmbeddedTurnMaxMbps  int64

	// WebDir is the path to the directory containing static web files.
	WebDir string
}

// Default returns a Config with bounded public-service defaults.
func Default() *Config {
	return &Config{
		ListenAddr:                      ":8080",
		MaxBytes:                        defaultMaxTransferBytes,
		ReadHeaderTimeout:               10 * time.Second,
		ControlTimeout:                  30 * time.Second,
		TransferTimeout:                 5 * time.Minute,
		IdleTimeout:                     2 * time.Minute,
		EnableServerTiming:              true,
		EnableCORS:                      true,
		AllowedOrigins:                  []string{"*"},
		CORSAllowCredentials:            false,
		Hostname:                        "localhost",
		Colo:                            "LOCAL",
		MaxConcurrentTransfers:          256,
		MaxConcurrentTransfersPerClient: 24,
		ClientBandwidthQuotaBytes:       defaultClientBandwidthQuotaBytes,
		ClientBandwidthQuotaWindow:      time.Hour,
		MaxWebRTCSessions:               64,
		MaxWebRTCSessionsPerClient:      2,
		WebRTCOfferRatePerMinute:        12,
		WebRTCOfferBurst:                4,
		TurnCredentialRatePerMinute:     60,
		TurnCredentialBurst:             10,
		MaxOfferBodyBytes:               defaultOfferBodyBytes,
		MaxReportBodyBytes:              defaultReportBodyBytes,
		EnableMetrics:                   false,
		MaxTurnTTL:                      600,
		EmbeddedTurn:                    false,
		EmbeddedTurnAddr:                "127.0.0.1:3478",
		EmbeddedTurnMaxMbps:             100,
		TurnRealm:                       "netspeed",
	}
}

// FromEnv loads configuration from environment variables, falling back to
// defaults. Invalid environment values are returned as errors rather than being
// silently ignored.
func FromEnv() (*Config, error) {
	cfg := Default()
	if err := ApplyEnv(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ApplyEnv overlays NETSPEEDD_* environment variables onto cfg.
func ApplyEnv(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}

	setStringEnv(&cfg.ListenAddr, "NETSPEEDD_LISTEN_ADDR")
	setStringEnv(&cfg.TLSCertFile, "NETSPEEDD_TLS_CERT")
	setStringEnv(&cfg.TLSKeyFile, "NETSPEEDD_TLS_KEY")
	if err := setInt64Env(&cfg.MaxBytes, "NETSPEEDD_MAX_BYTES", positive); err != nil {
		return err
	}
	if err := setDurationEnv(&cfg.ReadHeaderTimeout, "NETSPEEDD_READ_HEADER_TIMEOUT"); err != nil {
		return err
	}
	if err := setDurationEnv(&cfg.ControlTimeout, "NETSPEEDD_CONTROL_TIMEOUT"); err != nil {
		return err
	}
	if err := setDurationEnv(&cfg.TransferTimeout, "NETSPEEDD_TRANSFER_TIMEOUT"); err != nil {
		return err
	}
	if err := setDurationEnv(&cfg.IdleTimeout, "NETSPEEDD_IDLE_TIMEOUT"); err != nil {
		return err
	}
	if err := setBoolEnv(&cfg.EnableServerTiming, "NETSPEEDD_SERVER_TIMING"); err != nil {
		return err
	}
	if err := setBoolEnv(&cfg.EnableCORS, "NETSPEEDD_ENABLE_CORS"); err != nil {
		return err
	}
	if values, ok := splitEnv("NETSPEEDD_ALLOWED_ORIGINS"); ok {
		cfg.AllowedOrigins = values
	}
	if err := setBoolEnv(&cfg.CORSAllowCredentials, "NETSPEEDD_CORS_ALLOW_CREDENTIALS"); err != nil {
		return err
	}
	setStringEnv(&cfg.LocationsFile, "NETSPEEDD_LOCATIONS_FILE")
	setStringEnv(&cfg.GeoIPASNDatabasePath, "NETSPEEDD_GEOIP_ASN_DB")
	setStringEnv(&cfg.GeoIPCityDatabasePath, "NETSPEEDD_GEOIP_CITY_DB")
	// NETSPEEDD_GEOIP_DB is retained as an ASN-only compatibility alias.
	// Use it only when the explicit ASN database path is unset.
	if cfg.GeoIPASNDatabasePath == "" {
		setStringEnv(&cfg.GeoIPASNDatabasePath, "NETSPEEDD_GEOIP_DB")
	}
	if err := setBoolEnv(&cfg.TrustProxyHeaders, "NETSPEEDD_TRUST_PROXY"); err != nil {
		return err
	}
	if proxies, ok := splitEnv("NETSPEEDD_TRUSTED_PROXY_CIDRS"); ok {
		cfg.TrustedProxyCIDRs = proxies
		cfg.TrustProxyHeaders = len(proxies) > 0
	}
	setStringEnv(&cfg.Hostname, "NETSPEEDD_HOSTNAME")
	setStringEnv(&cfg.Colo, "NETSPEEDD_COLO")

	if err := setIntEnv(&cfg.MaxConcurrentTransfers, "NETSPEEDD_MAX_CONCURRENT_TRANSFERS", positive); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.MaxConcurrentTransfersPerClient, "NETSPEEDD_MAX_CONCURRENT_TRANSFERS_PER_CLIENT", positive); err != nil {
		return err
	}
	if err := setInt64Env(&cfg.ClientBandwidthQuotaBytes, "NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_BYTES", nonNegative); err != nil {
		return err
	}
	if err := setDurationEnv(&cfg.ClientBandwidthQuotaWindow, "NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_WINDOW"); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.MaxWebRTCSessions, "NETSPEEDD_MAX_WEBRTC_SESSIONS", positive); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.MaxWebRTCSessionsPerClient, "NETSPEEDD_MAX_WEBRTC_SESSIONS_PER_CLIENT", positive); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.WebRTCOfferRatePerMinute, "NETSPEEDD_WEBRTC_OFFER_RATE_PER_MINUTE", nonNegative); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.WebRTCOfferBurst, "NETSPEEDD_WEBRTC_OFFER_BURST", positive); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.TurnCredentialRatePerMinute, "NETSPEEDD_TURN_CREDENTIAL_RATE_PER_MINUTE", nonNegative); err != nil {
		return err
	}
	if err := setIntEnv(&cfg.TurnCredentialBurst, "NETSPEEDD_TURN_CREDENTIAL_BURST", positive); err != nil {
		return err
	}
	if err := setInt64Env(&cfg.MaxOfferBodyBytes, "NETSPEEDD_MAX_OFFER_BODY_BYTES", positive); err != nil {
		return err
	}
	if err := setInt64Env(&cfg.MaxReportBodyBytes, "NETSPEEDD_MAX_REPORT_BODY_BYTES", positive); err != nil {
		return err
	}
	setStringEnv(&cfg.AccessToken, "NETSPEEDD_ACCESS_TOKEN")
	setStringEnv(&cfg.MetricsToken, "NETSPEEDD_METRICS_TOKEN")
	if err := setBoolEnv(&cfg.EnableMetrics, "NETSPEEDD_ENABLE_METRICS"); err != nil {
		return err
	}

	setStringEnv(&cfg.TurnSecret, "NETSPEEDD_TURN_SECRET")
	setStringEnv(&cfg.TurnRealm, "NETSPEEDD_TURN_REALM")
	if servers, ok := splitEnv("NETSPEEDD_TURN_SERVERS"); ok {
		cfg.TurnServers = servers
	}
	if err := setInt64Env(&cfg.MaxTurnTTL, "NETSPEEDD_MAX_TURN_TTL", positive); err != nil {
		return err
	}
	if err := setBoolEnv(&cfg.EmbeddedTurn, "NETSPEEDD_EMBEDDED_TURN"); err != nil {
		return err
	}
	setStringEnv(&cfg.EmbeddedTurnAddr, "NETSPEEDD_EMBEDDED_TURN_ADDR")
	setStringEnv(&cfg.EmbeddedTurnPublicIP, "NETSPEEDD_EMBEDDED_TURN_PUBLIC_IP")
	if err := setInt64Env(&cfg.EmbeddedTurnMaxMbps, "NETSPEEDD_EMBEDDED_TURN_MAX_MBPS", positive); err != nil {
		return err
	}
	setStringEnv(&cfg.WebDir, "NETSPEEDD_WEB_DIR")

	return nil
}

// Validate rejects unsafe or internally inconsistent configuration.
func (cfg *Config) Validate() error {
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return fmt.Errorf("listen address is required")
	}
	if (strings.TrimSpace(cfg.TLSCertFile) == "") != (strings.TrimSpace(cfg.TLSKeyFile) == "") {
		return fmt.Errorf("TLS certificate and key must be configured together")
	}
	if cfg.MaxBytes <= 0 {
		return fmt.Errorf("maximum transfer bytes must be positive")
	}
	if cfg.ReadHeaderTimeout <= 0 || cfg.ControlTimeout <= 0 || cfg.TransferTimeout <= 0 || cfg.IdleTimeout <= 0 {
		return fmt.Errorf("HTTP timeouts must be positive")
	}
	if cfg.EnableCORS {
		if len(cfg.AllowedOrigins) == 0 {
			return fmt.Errorf("CORS requires at least one allowed origin")
		}
		for _, origin := range cfg.AllowedOrigins {
			if err := validateOrigin(origin); err != nil {
				return err
			}
			if cfg.CORSAllowCredentials && strings.TrimSpace(origin) == "*" {
				return fmt.Errorf("credentialed CORS cannot use wildcard origins")
			}
		}
	} else if cfg.CORSAllowCredentials {
		return fmt.Errorf("CORS credentials cannot be enabled when CORS is disabled")
	}
	if cfg.MaxConcurrentTransfers <= 0 || cfg.MaxConcurrentTransfersPerClient <= 0 {
		return fmt.Errorf("transfer concurrency limits must be positive")
	}
	if cfg.MaxConcurrentTransfersPerClient < 2 {
		return fmt.Errorf("per-client transfer limit must be at least 2 for loaded-latency measurement")
	}
	if cfg.MaxConcurrentTransfersPerClient > cfg.MaxConcurrentTransfers {
		return fmt.Errorf("per-client transfer limit %d exceeds global limit %d", cfg.MaxConcurrentTransfersPerClient, cfg.MaxConcurrentTransfers)
	}
	if cfg.ClientBandwidthQuotaBytes < 0 {
		return fmt.Errorf("client bandwidth quota cannot be negative")
	}
	if cfg.ClientBandwidthQuotaBytes > 0 && cfg.ClientBandwidthQuotaWindow <= 0 {
		return fmt.Errorf("client bandwidth quota window must be positive")
	}
	if cfg.MaxWebRTCSessions <= 0 || cfg.MaxWebRTCSessionsPerClient <= 0 {
		return fmt.Errorf("WebRTC session limits must be positive")
	}
	if cfg.MaxWebRTCSessionsPerClient > cfg.MaxWebRTCSessions {
		return fmt.Errorf("per-client WebRTC session limit %d exceeds global limit %d", cfg.MaxWebRTCSessionsPerClient, cfg.MaxWebRTCSessions)
	}
	if err := validateRate("WebRTC offer", cfg.WebRTCOfferRatePerMinute, cfg.WebRTCOfferBurst); err != nil {
		return err
	}
	if err := validateRate("TURN credential", cfg.TurnCredentialRatePerMinute, cfg.TurnCredentialBurst); err != nil {
		return err
	}
	if cfg.MaxOfferBodyBytes <= 0 || cfg.MaxReportBodyBytes <= 0 {
		return fmt.Errorf("control-plane request-body limits must be positive")
	}
	if cfg.TrustProxyHeaders && len(cfg.TrustedProxyCIDRs) == 0 {
		return fmt.Errorf("proxy headers cannot be trusted without NETSPEEDD_TRUSTED_PROXY_CIDRS")
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}
	}
	if cfg.AccessToken != "" && len(cfg.AccessToken) < minimumSecretLength {
		return fmt.Errorf("access token must contain at least %d bytes", minimumSecretLength)
	}
	if cfg.MetricsToken != "" && len(cfg.MetricsToken) < minimumSecretLength {
		return fmt.Errorf("metrics token must contain at least %d bytes", minimumSecretLength)
	}
	if cfg.EnableMetrics && cfg.MetricsToken == "" && cfg.AccessToken == "" {
		return fmt.Errorf("metrics require NETSPEEDD_METRICS_TOKEN or NETSPEEDD_ACCESS_TOKEN")
	}
	if cfg.MaxTurnTTL < 60 {
		return fmt.Errorf("maximum TURN credential TTL must be at least 60 seconds")
	}
	if strings.TrimSpace(cfg.TurnRealm) == "" {
		return fmt.Errorf("TURN realm is required")
	}
	if cfg.TurnSecret != "" && len(cfg.TurnSecret) < minimumSecretLength {
		return fmt.Errorf("TURN secret must contain at least %d bytes", minimumSecretLength)
	}

	hasExternalTurn := false
	for _, raw := range cfg.TurnServers {
		server := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case strings.HasPrefix(server, "stun:"), strings.HasPrefix(server, "stuns:"):
		case strings.HasPrefix(server, "turn:"), strings.HasPrefix(server, "turns:"):
			hasExternalTurn = true
		default:
			return fmt.Errorf("invalid ICE server URL %q", raw)
		}
	}
	if hasExternalTurn && cfg.TurnSecret == "" {
		return fmt.Errorf("external TURN URLs require NETSPEEDD_TURN_SECRET")
	}
	if cfg.TurnSecret != "" && !cfg.EmbeddedTurn && len(cfg.TurnServers) == 0 {
		return fmt.Errorf("TURN secret configured without any TURN servers")
	}

	if cfg.EmbeddedTurn {
		if len(cfg.TurnServers) > 0 && cfg.EmbeddedTurnPort == "" {
			return fmt.Errorf("embedded TURN and user-configured external TURN servers are mutually exclusive")
		}
		if cfg.EmbeddedTurnMaxMbps <= 0 {
			return fmt.Errorf("embedded TURN rate limit must be positive")
		}
		host, _, err := net.SplitHostPort(cfg.EmbeddedTurnAddr)
		if err != nil {
			return fmt.Errorf("invalid embedded TURN listen address %q: %w", cfg.EmbeddedTurnAddr, err)
		}
		listenIP := net.ParseIP(strings.Trim(host, "[]"))
		if listenIP == nil {
			return fmt.Errorf("embedded TURN listen host must be an IP address")
		}
		if cfg.EmbeddedTurnPublicIP != "" && net.ParseIP(strings.Trim(cfg.EmbeddedTurnPublicIP, "[]")) == nil {
			return fmt.Errorf("embedded TURN public address must be an IP address")
		}
		if !listenIP.IsLoopback() && cfg.EmbeddedTurnPublicIP == "" {
			return fmt.Errorf("non-loopback embedded TURN requires NETSPEEDD_EMBEDDED_TURN_PUBLIC_IP")
		}
	}
	return nil
}

func validateOrigin(raw string) error {
	origin := strings.TrimSpace(raw)
	if origin == "*" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid CORS origin %q", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("CORS origin %q must use http or https", raw)
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("CORS origin %q must contain only scheme and authority", raw)
	}
	return nil
}

func validateRate(name string, perMinute, burst int) error {
	if perMinute < 0 {
		return fmt.Errorf("%s rate cannot be negative", name)
	}
	if perMinute > 0 && burst <= 0 {
		return fmt.Errorf("%s burst must be positive when rate limiting is enabled", name)
	}
	return nil
}

// TLSEnabled returns true if TLS certificate and key are configured.
func (cfg *Config) TLSEnabled() bool {
	return strings.TrimSpace(cfg.TLSCertFile) != "" && strings.TrimSpace(cfg.TLSKeyFile) != ""
}

type numericConstraint int

const (
	positive numericConstraint = iota
	nonNegative
)

func setStringEnv(target *string, name string) {
	if value, ok := os.LookupEnv(name); ok {
		*target = strings.TrimSpace(value)
	}
}

func setBoolEnv(target *bool, name string) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s: invalid boolean %q", name, value)
	}
	*target = parsed
	return nil
}

func setDurationEnv(target *time.Duration, name string) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s: invalid positive duration %q", name, value)
	}
	*target = parsed
	return nil
}

func setIntEnv(target *int, name string, constraint numericConstraint) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || (constraint == positive && parsed <= 0) || (constraint == nonNegative && parsed < 0) {
		return fmt.Errorf("%s: invalid integer %q", name, value)
	}
	*target = parsed
	return nil
}

func setInt64Env(target *int64, name string, constraint numericConstraint) error {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || (constraint == positive && parsed <= 0) || (constraint == nonNegative && parsed < 0) {
		return fmt.Errorf("%s: invalid integer %q", name, value)
	}
	*target = parsed
	return nil
}

func splitEnv(name string) ([]string, bool) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result, true
}
