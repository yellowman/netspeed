// Package config provides configuration structures and loading for netspeedd.
package config

import (
	"fmt"
	"net"
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
	// ListenAddr is the address to listen on (e.g., ":8080" or "0.0.0.0:443").
	ListenAddr string

	// TLS configuration - if both are empty, server runs in HTTP-only mode.
	TLSCertFile string
	TLSKeyFile  string

	// MaxBytes is the hard cap for bytes parameter in /__down and upload body size.
	MaxBytes int64

	// HTTP server timeouts.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// EnableServerTiming adds Server-Timing headers to responses.
	EnableServerTiming bool

	// CORS configuration.
	EnableCORS     bool
	AllowedOrigins []string

	// LocationsFile is the path to JSON file containing Location list.
	LocationsFile string

	// Meta/geo configuration. Forwarding headers are honored only when the
	// direct peer is inside TrustedProxyCIDRs.
	GeoIPDatabasePath string
	TrustProxyHeaders bool // retained as an explicit enable/deprecation guard
	TrustedProxyCIDRs []string

	// Hostname to return in /meta response.
	Hostname string

	// Server location (colo) - IATA code.
	Colo string

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
		ReadTimeout:                     15 * time.Second,
		WriteTimeout:                    60 * time.Second,
		IdleTimeout:                     120 * time.Second,
		EnableServerTiming:              true,
		EnableCORS:                      true,
		AllowedOrigins:                  []string{"*"},
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

// FromEnv loads configuration from environment variables, falling back to defaults.
func FromEnv() *Config {
	cfg := Default()

	setString(&cfg.ListenAddr, "NETSPEEDD_LISTEN_ADDR")
	setString(&cfg.TLSCertFile, "NETSPEEDD_TLS_CERT")
	setString(&cfg.TLSKeyFile, "NETSPEEDD_TLS_KEY")
	setPositiveInt64(&cfg.MaxBytes, "NETSPEEDD_MAX_BYTES")
	setDuration(&cfg.ReadTimeout, "NETSPEEDD_READ_TIMEOUT")
	setDuration(&cfg.WriteTimeout, "NETSPEEDD_WRITE_TIMEOUT")
	setDuration(&cfg.IdleTimeout, "NETSPEEDD_IDLE_TIMEOUT")
	setBool(&cfg.EnableServerTiming, "NETSPEEDD_SERVER_TIMING")
	setBool(&cfg.EnableCORS, "NETSPEEDD_ENABLE_CORS")
	if origins := splitEnv("NETSPEEDD_ALLOWED_ORIGINS"); len(origins) > 0 {
		cfg.AllowedOrigins = origins
	}
	setString(&cfg.LocationsFile, "NETSPEEDD_LOCATIONS_FILE")
	setString(&cfg.GeoIPDatabasePath, "NETSPEEDD_GEOIP_DB")
	setBool(&cfg.TrustProxyHeaders, "NETSPEEDD_TRUST_PROXY")
	if proxies := splitEnv("NETSPEEDD_TRUSTED_PROXY_CIDRS"); len(proxies) > 0 {
		cfg.TrustedProxyCIDRs = proxies
		cfg.TrustProxyHeaders = true
	}
	setString(&cfg.Hostname, "NETSPEEDD_HOSTNAME")
	setString(&cfg.Colo, "NETSPEEDD_COLO")

	setPositiveInt(&cfg.MaxConcurrentTransfers, "NETSPEEDD_MAX_CONCURRENT_TRANSFERS")
	setPositiveInt(&cfg.MaxConcurrentTransfersPerClient, "NETSPEEDD_MAX_CONCURRENT_TRANSFERS_PER_CLIENT")
	setNonNegativeInt64(&cfg.ClientBandwidthQuotaBytes, "NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_BYTES")
	setDuration(&cfg.ClientBandwidthQuotaWindow, "NETSPEEDD_CLIENT_BANDWIDTH_QUOTA_WINDOW")
	setPositiveInt(&cfg.MaxWebRTCSessions, "NETSPEEDD_MAX_WEBRTC_SESSIONS")
	setPositiveInt(&cfg.MaxWebRTCSessionsPerClient, "NETSPEEDD_MAX_WEBRTC_SESSIONS_PER_CLIENT")
	setNonNegativeInt(&cfg.WebRTCOfferRatePerMinute, "NETSPEEDD_WEBRTC_OFFER_RATE_PER_MINUTE")
	setPositiveInt(&cfg.WebRTCOfferBurst, "NETSPEEDD_WEBRTC_OFFER_BURST")
	setNonNegativeInt(&cfg.TurnCredentialRatePerMinute, "NETSPEEDD_TURN_CREDENTIAL_RATE_PER_MINUTE")
	setPositiveInt(&cfg.TurnCredentialBurst, "NETSPEEDD_TURN_CREDENTIAL_BURST")
	setPositiveInt64(&cfg.MaxOfferBodyBytes, "NETSPEEDD_MAX_OFFER_BODY_BYTES")
	setPositiveInt64(&cfg.MaxReportBodyBytes, "NETSPEEDD_MAX_REPORT_BODY_BYTES")
	setString(&cfg.AccessToken, "NETSPEEDD_ACCESS_TOKEN")
	setString(&cfg.MetricsToken, "NETSPEEDD_METRICS_TOKEN")
	setBool(&cfg.EnableMetrics, "NETSPEEDD_ENABLE_METRICS")

	setString(&cfg.TurnSecret, "NETSPEEDD_TURN_SECRET")
	setString(&cfg.TurnRealm, "NETSPEEDD_TURN_REALM")
	if servers := splitEnv("NETSPEEDD_TURN_SERVERS"); len(servers) > 0 {
		cfg.TurnServers = servers
	}
	setPositiveInt64(&cfg.MaxTurnTTL, "NETSPEEDD_MAX_TURN_TTL")
	setBool(&cfg.EmbeddedTurn, "NETSPEEDD_EMBEDDED_TURN")
	setString(&cfg.EmbeddedTurnAddr, "NETSPEEDD_EMBEDDED_TURN_ADDR")
	setString(&cfg.EmbeddedTurnPublicIP, "NETSPEEDD_EMBEDDED_TURN_PUBLIC_IP")
	setPositiveInt64(&cfg.EmbeddedTurnMaxMbps, "NETSPEEDD_EMBEDDED_TURN_MAX_MBPS")
	setString(&cfg.WebDir, "NETSPEEDD_WEB_DIR")

	return cfg
}

// Validate rejects unsafe or internally inconsistent Phase 4 configuration.
func (cfg *Config) Validate() error {
	if strings.TrimSpace(cfg.ListenAddr) == "" {
		return fmt.Errorf("listen address is required")
	}
	if cfg.MaxBytes <= 0 {
		return fmt.Errorf("maximum transfer bytes must be positive")
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
	return cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
}

func setString(target *string, name string) {
	if value := os.Getenv(name); value != "" {
		*target = value
	}
}

func setBool(target *bool, name string) {
	if value := os.Getenv(name); value != "" {
		*target = value == "true" || value == "1"
	}
}

func setDuration(target *time.Duration, name string) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			*target = parsed
		}
	}
}

func setPositiveInt(target *int, name string) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			*target = parsed
		}
	}
}

func setNonNegativeInt(target *int, name string) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			*target = parsed
		}
	}
}

func setPositiveInt64(target *int64, name string) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
			*target = parsed
		}
	}
}

func setNonNegativeInt64(target *int64, name string) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 {
			*target = parsed
		}
	}
}

func splitEnv(name string) []string {
	value := os.Getenv(name)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
