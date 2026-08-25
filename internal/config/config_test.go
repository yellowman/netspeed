package config

import (
	"strings"
	"testing"
)

func TestDefaultPhase4ConfigurationIsValidAndTURNIsOptIn(t *testing.T) {
	cfg := Default()
	if cfg.EmbeddedTurn {
		t.Fatal("embedded TURN must be opt-in")
	}
	if cfg.EmbeddedTurnAddr != "127.0.0.1:3478" {
		t.Fatalf("embedded TURN address=%q; want loopback", cfg.EmbeddedTurnAddr)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default validation: %v", err)
	}
}

func TestValidateRequiresProxyCIDRs(t *testing.T) {
	cfg := Default()
	cfg.TrustProxyHeaders = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "CIDR") {
		t.Fatalf("validation error=%v; want trusted CIDR requirement", err)
	}

	cfg.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("trusted proxy validation: %v", err)
	}
}

func TestValidateRejectsPublicEmbeddedTURNWithoutPublicIP(t *testing.T) {
	cfg := Default()
	cfg.EmbeddedTurn = true
	cfg.EmbeddedTurnAddr = "0.0.0.0:3478"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "PUBLIC_IP") {
		t.Fatalf("validation error=%v; want public IP requirement", err)
	}

	cfg.EmbeddedTurnPublicIP = "203.0.113.10"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("public embedded TURN validation: %v", err)
	}
}

func TestValidateAllowsCustomEmbeddedTURNSecret(t *testing.T) {
	cfg := Default()
	cfg.EmbeddedTurn = true
	cfg.TurnSecret = "0123456789abcdef"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("embedded TURN with custom secret: %v", err)
	}
}

func TestValidateAllowsSTUNOnlyConfiguration(t *testing.T) {
	cfg := Default()
	cfg.TurnServers = []string{"stun:stun.example.test:3478", "stuns:stun.example.test:5349"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("STUN-only validation: %v", err)
	}
}

func TestValidateRequiresSecretForExternalTURN(t *testing.T) {
	cfg := Default()
	cfg.TurnServers = []string{"stun:stun.example.test:3478", "turn:turn.example.test:3478?transport=udp"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "TURN_SECRET") {
		t.Fatalf("validation error=%v; want secret requirement", err)
	}
	cfg.TurnSecret = "0123456789abcdef"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("external TURN validation: %v", err)
	}
}

func TestValidateRejectsLimitInversions(t *testing.T) {
	cfg := Default()
	cfg.MaxConcurrentTransfers = 4
	cfg.MaxConcurrentTransfersPerClient = 5
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected transfer limit inversion error")
	}

	cfg = Default()
	cfg.MaxWebRTCSessions = 1
	cfg.MaxWebRTCSessionsPerClient = 2
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected WebRTC limit inversion error")
	}
}

func TestMetricsAreOptInAndRequireAuthentication(t *testing.T) {
	cfg := Default()
	if cfg.EnableMetrics {
		t.Fatal("metrics must be opt-in")
	}
	cfg.EnableMetrics = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "metrics require") {
		t.Fatalf("validation error=%v; want metrics authentication requirement", err)
	}
	cfg.MetricsToken = "0123456789abcdef"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("authenticated metrics validation: %v", err)
	}
}

func TestValidateRequiresTwoPerClientTransferSlots(t *testing.T) {
	cfg := Default()
	cfg.MaxConcurrentTransfersPerClient = 1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("validation error=%v; want loaded-latency concurrency requirement", err)
	}
}

func TestValidateRejectsOrphanTURNSecret(t *testing.T) {
	cfg := Default()
	cfg.TurnSecret = "0123456789abcdef"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "without any TURN servers") {
		t.Fatalf("validation error=%v; want orphan TURN secret rejection", err)
	}
}

func TestDefaultPhase5TimeoutPolicy(t *testing.T) {
	cfg := Default()
	if cfg.ReadHeaderTimeout <= 0 || cfg.ControlTimeout <= 0 || cfg.TransferTimeout <= 0 || cfg.IdleTimeout <= 0 {
		t.Fatalf("invalid default timeout policy: %+v", cfg)
	}
	if cfg.TransferTimeout <= cfg.ControlTimeout {
		t.Fatalf("transfer timeout %s must exceed control timeout %s", cfg.TransferTimeout, cfg.ControlTimeout)
	}
}

func TestValidateRejectsPartialTLSConfiguration(t *testing.T) {
	cfg := Default()
	cfg.TLSCertFile = "server.crt"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("certificate-only validation error=%v; want paired TLS rejection", err)
	}

	cfg = Default()
	cfg.TLSKeyFile = "server.key"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("key-only validation error=%v; want paired TLS rejection", err)
	}
}

func TestValidateCredentialedCORSRequiresExplicitOrigins(t *testing.T) {
	cfg := Default()
	cfg.CORSAllowCredentials = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("credentialed wildcard validation error=%v; want rejection", err)
	}

	cfg.AllowedOrigins = []string{"https://ui.example.test"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit credentialed origin validation: %v", err)
	}
}

func TestFromEnvRejectsMalformedValues(t *testing.T) {
	t.Setenv("NETSPEEDD_TRANSFER_TIMEOUT", "not-a-duration")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "NETSPEEDD_TRANSFER_TIMEOUT") {
		t.Fatalf("FromEnv error=%v; want malformed duration rejection", err)
	}
}

func TestFromEnvLoadsPhase5DeploymentFields(t *testing.T) {
	t.Setenv("NETSPEEDD_READ_HEADER_TIMEOUT", "7s")
	t.Setenv("NETSPEEDD_CONTROL_TIMEOUT", "20s")
	t.Setenv("NETSPEEDD_TRANSFER_TIMEOUT", "4m")
	t.Setenv("NETSPEEDD_CORS_ALLOW_CREDENTIALS", "true")
	t.Setenv("NETSPEEDD_ALLOWED_ORIGINS", "https://ui.example.test")
	t.Setenv("NETSPEEDD_GEOIP_ASN_DB", "/data/asn.mmdb")
	t.Setenv("NETSPEEDD_GEOIP_CITY_DB", "/data/city.mmdb")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.ReadHeaderTimeout.String() != "7s" || cfg.ControlTimeout.String() != "20s" || cfg.TransferTimeout.String() != "4m0s" {
		t.Fatalf("unexpected timeouts: header=%s control=%s transfer=%s", cfg.ReadHeaderTimeout, cfg.ControlTimeout, cfg.TransferTimeout)
	}
	if !cfg.CORSAllowCredentials || len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "https://ui.example.test" {
		t.Fatalf("unexpected CORS config: credentials=%v origins=%v", cfg.CORSAllowCredentials, cfg.AllowedOrigins)
	}
	if cfg.GeoIPASNDatabasePath != "/data/asn.mmdb" || cfg.GeoIPCityDatabasePath != "/data/city.mmdb" {
		t.Fatalf("unexpected GeoIP paths: asn=%q city=%q", cfg.GeoIPASNDatabasePath, cfg.GeoIPCityDatabasePath)
	}
}

func TestValidateRejectsOriginWithPath(t *testing.T) {
	cfg := Default()
	cfg.AllowedOrigins = []string{"https://ui.example.test/"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "scheme and authority") {
		t.Fatalf("validation error=%v; want origin-path rejection", err)
	}
}
