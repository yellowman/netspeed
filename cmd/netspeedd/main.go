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
	// Define command-line flags
	var (
		listenAddr     = flag.String("listen", "", "Listen address (default :8080)")
		tlsCert        = flag.String("tls-cert", "", "TLS certificate file path")
		tlsKey         = flag.String("tls-key", "", "TLS key file path")
		maxBytes       = flag.Int64("max-bytes", 0, "Maximum bytes for download/upload (default 1GiB)")
		locationsFile  = flag.String("locations", "", "Path to locations JSON file")
		hostname       = flag.String("hostname", "", "Hostname to return in /meta")
		colo           = flag.String("colo", "", "Server colo/datacenter IATA code")
		trustProxy     = flag.Bool("trust-proxy", false, "Trust X-Forwarded-For headers")
		enableCORS     = flag.Bool("cors", true, "Enable CORS headers")
		corsOrigins    = flag.String("cors-origins", "*", "Allowed CORS origins (comma-separated)")
		serverTiming   = flag.Bool("server-timing", true, "Enable Server-Timing headers")
		turnSecret     = flag.String("turn-secret", "", "TURN server shared secret")
		turnServers    = flag.String("turn-servers", "", "TURN servers (comma-separated)")
		turnRealm      = flag.String("turn-realm", "", "TURN realm")
		embeddedTurn   = flag.Bool("embedded-turn", true, "Enable embedded TURN server")
		embeddedTurnAddr = flag.String("embedded-turn-addr", "", "Embedded TURN server address (default 0.0.0.0:3478)")
		embeddedTurnIP = flag.String("embedded-turn-ip", "", "Public IP for embedded TURN server")
		webDir         = flag.String("web-dir", "", "Directory containing static web files")
		showVersion    = flag.Bool("version", false, "Show version information")
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "netspeedd - Speedtest backend server\n\n")
		fmt.Fprintf(os.Stderr, "Usage: netspeedd [options]\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nEnvironment variables:\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_LISTEN_ADDR     Listen address\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_TLS_CERT        TLS certificate file\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_TLS_KEY         TLS key file\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_MAX_BYTES       Maximum bytes\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_LOCATIONS_FILE  Locations JSON file\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_HOSTNAME        Hostname for /meta\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_COLO            Datacenter IATA code\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_TRUST_PROXY     Trust proxy headers (true/false)\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_ENABLE_CORS     Enable CORS (true/false)\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_ALLOWED_ORIGINS Allowed CORS origins\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_SERVER_TIMING   Enable Server-Timing (true/false)\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_TURN_SECRET     TURN shared secret\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_TURN_SERVERS    TURN servers\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_TURN_REALM      TURN realm\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_EMBEDDED_TURN   Enable embedded TURN (true/false)\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_EMBEDDED_TURN_ADDR Embedded TURN address\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_EMBEDDED_TURN_PUBLIC_IP Public IP for TURN\n")
		fmt.Fprintf(os.Stderr, "  NETSPEEDD_WEB_DIR         Static web files directory\n")
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("netspeedd version %s (commit: %s, built: %s)\n", version, commit, date)
		os.Exit(0)
	}

	// Load config from environment, then override with flags
	cfg := config.FromEnv()

	// Override with command-line flags
	if *listenAddr != "" {
		cfg.ListenAddr = *listenAddr
	}
	if *tlsCert != "" {
		cfg.TLSCertFile = *tlsCert
	}
	if *tlsKey != "" {
		cfg.TLSKeyFile = *tlsKey
	}
	if *maxBytes > 0 {
		cfg.MaxBytes = *maxBytes
	}
	if *locationsFile != "" {
		cfg.LocationsFile = *locationsFile
	}
	if *hostname != "" {
		cfg.Hostname = *hostname
	}
	if *colo != "" {
		cfg.Colo = *colo
	}
	cfg.TrustProxyHeaders = *trustProxy
	cfg.EnableCORS = *enableCORS
	if *corsOrigins != "*" {
		cfg.AllowedOrigins = strings.Split(*corsOrigins, ",")
	}
	cfg.EnableServerTiming = *serverTiming
	if *turnSecret != "" {
		cfg.TurnSecret = *turnSecret
	}
	if *turnServers != "" {
		cfg.TurnServers = strings.Split(*turnServers, ",")
	}
	if *turnRealm != "" {
		cfg.TurnRealm = *turnRealm
	}
	if *webDir != "" {
		cfg.WebDir = *webDir
	}
	// Handle embedded TURN flag - can disable via -embedded-turn=false
	cfg.EmbeddedTurn = *embeddedTurn
	if *embeddedTurnAddr != "" {
		cfg.EmbeddedTurnAddr = *embeddedTurnAddr
	}
	if *embeddedTurnIP != "" {
		cfg.EmbeddedTurnPublicIP = *embeddedTurnIP
	}

	// Start embedded TURN server if enabled and no external TURN configured
	var turnSrv *turnserver.Server
	if cfg.EmbeddedTurn && cfg.TurnSecret == "" && len(cfg.TurnServers) == 0 {
		// Determine public IP for TURN server
		publicIP := cfg.EmbeddedTurnPublicIP
		if publicIP == "" {
			// Try to get local IP
			publicIP = getLocalIP()
		}

		turnCfg := turnserver.Config{
			ListenAddr: cfg.EmbeddedTurnAddr,
			Realm:      cfg.TurnRealm,
			PublicIP:   publicIP,
		}

		var err error
		turnSrv, err = turnserver.New(turnCfg)
		if err != nil {
			log.Printf("Warning: Failed to start embedded TURN server: %v", err)
		} else {
			turnSrv.Start()
			// Configure the HTTP server to use the embedded TURN server
			cfg.TurnSecret = turnSrv.Secret()
			cfg.TurnRealm = turnSrv.Realm()
			// Extract port from listen address for dynamic URL generation
			turnAddr := turnSrv.ListenAddr()
			_, port, _ := net.SplitHostPort(turnAddr)
			if port == "" {
				port = "3478"
			}
			cfg.EmbeddedTurnPort = port
			// If public IP is set, use static URL; otherwise handler uses request host
			if publicIP != "" {
				cfg.TurnServers = []string{fmt.Sprintf("turn:%s:%s", publicIP, port)}
				log.Printf("Embedded TURN configured: servers=%v", cfg.TurnServers)
			} else {
				log.Printf("Embedded TURN configured on port %s (URL derived from request host)", port)
			}
		}
	}

	// Create server
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Run()
	}()

	// Wait for signal or error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		// Shutdown embedded TURN server if running
		if turnSrv != nil {
			if err := turnSrv.Close(); err != nil {
				log.Printf("Error shutting down TURN server: %v", err)
			}
		}
	case err := <-errChan:
		if err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}

	log.Println("Server stopped")
}

// getLocalIP returns the local IP address of the machine.
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}
