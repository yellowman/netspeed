// Package config provides configuration structures and loading for netspeedd.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the netspeedd server.
type Config struct {
	// ListenAddr is the address to listen on (e.g., ":8080" or "0.0.0.0:443")
	ListenAddr string

	// TLS configuration - if both are empty, server runs in HTTP-only mode
	TLSCertFile string
	TLSKeyFile  string

	// MaxBytes is the hard cap for bytes parameter in /__down and upload body size
	MaxBytes int64

	// HTTP server timeouts
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// EnableServerTiming adds Server-Timing headers to responses
	EnableServerTiming bool

	// CORS configuration
	EnableCORS     bool
	AllowedOrigins []string

	// LocationsFile is the path to JSON file containing Location list
	LocationsFile string

	// Meta/geo configuration
	GeoIPDatabasePath string
	TrustProxyHeaders bool

	// Hostname to return in /meta response
	Hostname string

	// Server location (colo) - IATA code
	Colo string

	// TURN server configuration
	TurnSecret    string
	TurnServers   []string
	TurnRealm     string
	MaxTurnTTL    int64

	// EmbeddedTurn enables the built-in TURN server
	EmbeddedTurn bool
	// EmbeddedTurnAddr is the address for the embedded TURN server
	EmbeddedTurnAddr string
	// EmbeddedTurnPublicIP is the public IP to advertise for the embedded TURN server
	EmbeddedTurnPublicIP string
	// EmbeddedTurnPort stores the port when embedded TURN is active (for dynamic URL generation)
	EmbeddedTurnPort string

	// WebDir is the path to the directory containing static web files
	// If set, the server will serve static files from this directory
	WebDir string
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	return &Config{
		ListenAddr:         ":8080",
		MaxBytes:           1 << 30, // 1 GiB
		ReadTimeout:        15 * time.Second,
		WriteTimeout:       60 * time.Second,
		IdleTimeout:        120 * time.Second,
		EnableServerTiming: true,
		EnableCORS:         true,
		AllowedOrigins:     []string{"*"},
		Hostname:           "localhost",
		Colo:               "LOCAL",
		MaxTurnTTL:         600,
		EmbeddedTurn:       true,
		EmbeddedTurnAddr:   "0.0.0.0:3478",
		TurnRealm:          "netspeed",
	}
}

// FromEnv loads configuration from environment variables, falling back to defaults.
func FromEnv() *Config {
	cfg := Default()

	if addr := os.Getenv("NETSPEEDD_LISTEN_ADDR"); addr != "" {
		cfg.ListenAddr = addr
	}

	if certFile := os.Getenv("NETSPEEDD_TLS_CERT"); certFile != "" {
		cfg.TLSCertFile = certFile
	}

	if keyFile := os.Getenv("NETSPEEDD_TLS_KEY"); keyFile != "" {
		cfg.TLSKeyFile = keyFile
	}

	if maxBytes := os.Getenv("NETSPEEDD_MAX_BYTES"); maxBytes != "" {
		if v, err := strconv.ParseInt(maxBytes, 10, 64); err == nil && v > 0 {
			cfg.MaxBytes = v
		}
	}

	if readTimeout := os.Getenv("NETSPEEDD_READ_TIMEOUT"); readTimeout != "" {
		if d, err := time.ParseDuration(readTimeout); err == nil {
			cfg.ReadTimeout = d
		}
	}

	if writeTimeout := os.Getenv("NETSPEEDD_WRITE_TIMEOUT"); writeTimeout != "" {
		if d, err := time.ParseDuration(writeTimeout); err == nil {
			cfg.WriteTimeout = d
		}
	}

	if idleTimeout := os.Getenv("NETSPEEDD_IDLE_TIMEOUT"); idleTimeout != "" {
		if d, err := time.ParseDuration(idleTimeout); err == nil {
			cfg.IdleTimeout = d
		}
	}

	if serverTiming := os.Getenv("NETSPEEDD_SERVER_TIMING"); serverTiming != "" {
		cfg.EnableServerTiming = serverTiming == "true" || serverTiming == "1"
	}

	if enableCORS := os.Getenv("NETSPEEDD_ENABLE_CORS"); enableCORS != "" {
		cfg.EnableCORS = enableCORS == "true" || enableCORS == "1"
	}

	if origins := os.Getenv("NETSPEEDD_ALLOWED_ORIGINS"); origins != "" {
		cfg.AllowedOrigins = []string{origins}
	}

	if locFile := os.Getenv("NETSPEEDD_LOCATIONS_FILE"); locFile != "" {
		cfg.LocationsFile = locFile
	}

	if geoDB := os.Getenv("NETSPEEDD_GEOIP_DB"); geoDB != "" {
		cfg.GeoIPDatabasePath = geoDB
	}

	if trustProxy := os.Getenv("NETSPEEDD_TRUST_PROXY"); trustProxy != "" {
		cfg.TrustProxyHeaders = trustProxy == "true" || trustProxy == "1"
	}

	if hostname := os.Getenv("NETSPEEDD_HOSTNAME"); hostname != "" {
		cfg.Hostname = hostname
	}

	if colo := os.Getenv("NETSPEEDD_COLO"); colo != "" {
		cfg.Colo = colo
	}

	if turnSecret := os.Getenv("NETSPEEDD_TURN_SECRET"); turnSecret != "" {
		cfg.TurnSecret = turnSecret
	}

	if turnRealm := os.Getenv("NETSPEEDD_TURN_REALM"); turnRealm != "" {
		cfg.TurnRealm = turnRealm
	}

	if turnServers := os.Getenv("NETSPEEDD_TURN_SERVERS"); turnServers != "" {
		cfg.TurnServers = []string{turnServers}
	}

	if maxTurnTTL := os.Getenv("NETSPEEDD_MAX_TURN_TTL"); maxTurnTTL != "" {
		if v, err := strconv.ParseInt(maxTurnTTL, 10, 64); err == nil && v > 0 {
			cfg.MaxTurnTTL = v
		}
	}

	if embeddedTurn := os.Getenv("NETSPEEDD_EMBEDDED_TURN"); embeddedTurn != "" {
		cfg.EmbeddedTurn = embeddedTurn == "true" || embeddedTurn == "1"
	}

	if embeddedTurnAddr := os.Getenv("NETSPEEDD_EMBEDDED_TURN_ADDR"); embeddedTurnAddr != "" {
		cfg.EmbeddedTurnAddr = embeddedTurnAddr
	}

	if embeddedTurnPublicIP := os.Getenv("NETSPEEDD_EMBEDDED_TURN_PUBLIC_IP"); embeddedTurnPublicIP != "" {
		cfg.EmbeddedTurnPublicIP = embeddedTurnPublicIP
	}

	if webDir := os.Getenv("NETSPEEDD_WEB_DIR"); webDir != "" {
		cfg.WebDir = webDir
	}

	return cfg
}

// TLSEnabled returns true if TLS certificate and key are configured.
func (c *Config) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}
