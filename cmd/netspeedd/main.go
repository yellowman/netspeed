// netspeedd is a Go-based speedtest backend that emulates the public API surface
// used by speed.cloudflare.com.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yellowman/netspeed/internal/config"
	"github.com/yellowman/netspeed/internal/server"
	turnserver "github.com/yellowman/netspeed/internal/turn"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	var (
		listenAddr         = flag.String("listen", "", "Listen address (default :8080)")
		tlsCert            = flag.String("tls-cert", "", "TLS certificate file path")
		tlsKey             = flag.String("tls-key", "", "TLS key file path")
		maxBytes           = flag.Int64("max-bytes", 0, "Maximum bytes for one download/upload")
		locationsFile      = flag.String("locations", "", "Path to locations JSON file")
		geoipDB            = flag.String("geoip-db", "", "Path to MaxMind GeoLite2-ASN.mmdb file")
		hostname           = flag.String("hostname", "", "Hostname returned by /meta")
		colo               = flag.String("colo", "", "Server colo/datacenter IATA code")
		trustProxy         = flag.Bool("trust-proxy", false, "Honor forwarding headers only from --trusted-proxies (deprecated enable flag)")
		trustedProxies     = flag.String("trusted-proxies", "", "Trusted proxy CIDRs (comma-separated)")
		enableCORS         = flag.Bool("cors", true, "Enable CORS headers")
		corsOrigins        = flag.String("cors-origins", "*", "Allowed CORS origins (comma-separated)")
		serverTiming       = flag.Bool("server-timing", true, "Enable Server-Timing headers")
		maxTransfers       = flag.Int("max-transfers", 0, "Global active measurement-transfer limit")
		maxClientTransfers = flag.Int("max-client-transfers", 0, "Active transfer limit per client")
		quotaBytes         = flag.Int64("client-quota-bytes", -1, "Per-client byte quota per window; 0 disables")
		quotaWindow        = flag.Duration("client-quota-window", 0, "Per-client byte quota window")
		maxSessions        = flag.Int("max-webrtc-sessions", 0, "Global active WebRTC session limit")
		maxClientSessions  = flag.Int("max-client-webrtc-sessions", 0, "Active WebRTC session limit per client")
		offerRate          = flag.Int("webrtc-offers-per-minute", -1, "WebRTC offer rate per client; 0 disables")
		offerBurst         = flag.Int("webrtc-offer-burst", 0, "WebRTC offer token-bucket burst")
		credentialRate     = flag.Int("turn-credentials-per-minute", -1, "TURN credential rate per client; 0 disables")
		credentialBurst    = flag.Int("turn-credential-burst", 0, "TURN credential token-bucket burst")
		maxOfferBody       = flag.Int64("max-offer-body", 0, "Maximum WebRTC offer JSON body bytes")
		maxReportBody      = flag.Int64("max-report-body", 0, "Maximum packet report JSON body bytes")
		accessToken        = flag.String("access-token", "", "Shared bearer token (prefer NETSPEEDD_ACCESS_TOKEN)")
		metricsToken       = flag.String("metrics-token", "", "Bearer token for /metrics (prefer environment)")
		enableMetrics      = flag.Bool("metrics", false, "Enable authenticated Prometheus /metrics endpoint")
		turnSecret         = flag.String("turn-secret", "", "TURN shared secret")
		turnServers        = flag.String("turn-servers", "", "External STUN/TURN URLs (comma-separated)")
		turnRealm          = flag.String("turn-realm", "", "TURN realm")
		embeddedTurn       = flag.Bool("embedded-turn", false, "Enable embedded TURN server (opt-in)")
		embeddedTurnAddr   = flag.String("embedded-turn-addr", "", "Embedded TURN listen address (default 127.0.0.1:3478)")
		embeddedTurnIP     = flag.String("embedded-turn-ip", "", "Public relay IP required for non-loopback TURN")
		embeddedTurnMbps   = flag.Int64("embedded-turn-max-mbps", 0, "Embedded TURN combined UDP rate ceiling")
		webDir             = flag.String("web-dir", "", "Directory containing static web files")
		showVersion        = flag.Bool("version", false, "Show version information")
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "netspeedd - Speedtest backend server\n\nUsage: netspeedd [options]\n\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nAll options also have NETSPEEDD_* environment equivalents documented in README.md.\n")
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("netspeedd version %s (commit: %s, built: %s)\n", version, commit, date)
		return
	}

	flagsSet := make(map[string]bool)
	flag.Visit(func(value *flag.Flag) { flagsSet[value.Name] = true })
	cfg := config.FromEnv()

	setFlagString(&cfg.ListenAddr, *listenAddr)
	setFlagString(&cfg.TLSCertFile, *tlsCert)
	setFlagString(&cfg.TLSKeyFile, *tlsKey)
	if *maxBytes > 0 {
		cfg.MaxBytes = *maxBytes
	}
	setFlagString(&cfg.LocationsFile, *locationsFile)
	setFlagString(&cfg.GeoIPDatabasePath, *geoipDB)
	setFlagString(&cfg.Hostname, *hostname)
	setFlagString(&cfg.Colo, *colo)
	if flagsSet["trust-proxy"] {
		cfg.TrustProxyHeaders = *trustProxy
		if !*trustProxy {
			cfg.TrustedProxyCIDRs = nil
		}
	}
	if flagsSet["trusted-proxies"] {
		cfg.TrustedProxyCIDRs = splitCSV(*trustedProxies)
		cfg.TrustProxyHeaders = len(cfg.TrustedProxyCIDRs) > 0
	}
	if flagsSet["cors"] {
		cfg.EnableCORS = *enableCORS
	}
	if flagsSet["cors-origins"] {
		cfg.AllowedOrigins = splitCSV(*corsOrigins)
	}
	if flagsSet["server-timing"] {
		cfg.EnableServerTiming = *serverTiming
	}
	if *maxTransfers > 0 {
		cfg.MaxConcurrentTransfers = *maxTransfers
	}
	if *maxClientTransfers > 0 {
		cfg.MaxConcurrentTransfersPerClient = *maxClientTransfers
	}
	if flagsSet["client-quota-bytes"] {
		cfg.ClientBandwidthQuotaBytes = *quotaBytes
	}
	if *quotaWindow > 0 {
		cfg.ClientBandwidthQuotaWindow = *quotaWindow
	}
	if *maxSessions > 0 {
		cfg.MaxWebRTCSessions = *maxSessions
	}
	if *maxClientSessions > 0 {
		cfg.MaxWebRTCSessionsPerClient = *maxClientSessions
	}
	if flagsSet["webrtc-offers-per-minute"] {
		cfg.WebRTCOfferRatePerMinute = *offerRate
	}
	if *offerBurst > 0 {
		cfg.WebRTCOfferBurst = *offerBurst
	}
	if flagsSet["turn-credentials-per-minute"] {
		cfg.TurnCredentialRatePerMinute = *credentialRate
	}
	if *credentialBurst > 0 {
		cfg.TurnCredentialBurst = *credentialBurst
	}
	if *maxOfferBody > 0 {
		cfg.MaxOfferBodyBytes = *maxOfferBody
	}
	if *maxReportBody > 0 {
		cfg.MaxReportBodyBytes = *maxReportBody
	}
	setFlagString(&cfg.AccessToken, *accessToken)
	setFlagString(&cfg.MetricsToken, *metricsToken)
	if flagsSet["metrics"] {
		cfg.EnableMetrics = *enableMetrics
	}
	setFlagString(&cfg.TurnSecret, *turnSecret)
	if flagsSet["turn-servers"] {
		cfg.TurnServers = splitCSV(*turnServers)
	}
	setFlagString(&cfg.TurnRealm, *turnRealm)
	if flagsSet["embedded-turn"] {
		cfg.EmbeddedTurn = *embeddedTurn
	}
	setFlagString(&cfg.EmbeddedTurnAddr, *embeddedTurnAddr)
	setFlagString(&cfg.EmbeddedTurnPublicIP, *embeddedTurnIP)
	if *embeddedTurnMbps > 0 {
		cfg.EmbeddedTurnMaxMbps = *embeddedTurnMbps
	}
	setFlagString(&cfg.WebDir, *webDir)

	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	var turnServer *turnserver.Server
	if cfg.EmbeddedTurn {
		var err error
		turnServer, err = turnserver.New(turnserver.Config{
			ListenAddr:       cfg.EmbeddedTurnAddr,
			Realm:            cfg.TurnRealm,
			Secret:           cfg.TurnSecret,
			PublicIP:         cfg.EmbeddedTurnPublicIP,
			MaxMbps:          cfg.EmbeddedTurnMaxMbps,
			MaxCredentialTTL: cfg.MaxTurnTTL,
		})
		if err != nil {
			log.Fatalf("Failed to start embedded TURN server: %v", err)
		}
		turnServer.Start()
		cfg.TurnSecret = turnServer.Secret()
		cfg.TurnRealm = turnServer.Realm()

		host, port, err := net.SplitHostPort(turnServer.ListenAddr())
		if err != nil {
			_ = turnServer.Close()
			log.Fatalf("Invalid embedded TURN listener address: %v", err)
		}
		advertisedIP := cfg.EmbeddedTurnPublicIP
		if advertisedIP == "" {
			advertisedIP = host
		}
		uriHost := formatURIHost(advertisedIP)
		cfg.EmbeddedTurnPort = port
		cfg.TurnServers = []string{
			fmt.Sprintf("stun:%s:%s", uriHost, port),
			fmt.Sprintf("turn:%s:%s?transport=udp", uriHost, port),
		}
		log.Printf("Embedded TURN listening on %s, advertised as %s, max=%d Mbps", turnServer.ListenAddr(), advertisedIP, cfg.EmbeddedTurnMaxMbps)
	}

	daemon, err := server.New(cfg)
	if err != nil {
		if turnServer != nil {
			_ = turnServer.Close()
		}
		log.Fatalf("Failed to create server: %v", err)
	}
	if turnServer != nil {
		daemon.SetRelayStatsProvider(turnServer)
	}

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGINT, syscall.SIGTERM)
	errorChannel := make(chan error, 1)
	go func() { errorChannel <- daemon.Run() }()

	select {
	case received := <-signalChannel:
		log.Printf("Received signal %v, shutting down...", received)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := daemon.Shutdown(ctx); err != nil {
			log.Printf("HTTP shutdown error: %v", err)
		}
		cancel()
		if turnServer != nil {
			if err := turnServer.Close(); err != nil {
				log.Printf("TURN shutdown error: %v", err)
			}
		}
	case err := <-errorChannel:
		if err != nil {
			log.Printf("Server stopped with error: %v", err)
		}
		if turnServer != nil {
			_ = turnServer.Close()
		}
	}
}

func setFlagString(target *string, value string) {
	if value != "" {
		*target = value
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func formatURIHost(host string) string {
	host = strings.Trim(host, "[]")
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
