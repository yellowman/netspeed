// Package server provides the HTTP server and request handling for netspeedd.
package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	pionwebrtc "github.com/pion/webrtc/v3"
	"github.com/yellowman/netspeed/internal/clientaddr"
	"github.com/yellowman/netspeed/internal/config"
	"github.com/yellowman/netspeed/internal/limits"
	"github.com/yellowman/netspeed/internal/locations"
	"github.com/yellowman/netspeed/internal/meta"
	"github.com/yellowman/netspeed/internal/telemetry"
	"github.com/yellowman/netspeed/internal/webrtc"
)

// Server is the main netspeedd HTTP server.
type Server struct {
	cfg        *config.Config
	httpServer *http.Server
	tlsConfig  *tls.Config

	metaProvider  meta.Provider
	geoipCloser   io.Closer
	locations     locations.Store
	payloadBuf    []byte
	webrtcManager *webrtc.Manager
	clientAddress *clientaddr.Resolver

	transferLimiter       *limits.TransferLimiter
	bandwidthQuota        *limits.ByteQuota
	offerRateLimiter      *limits.KeyedRateLimiter
	turnCredentialLimiter *limits.KeyedRateLimiter
	metrics               *serviceMetrics
	relayStats            telemetry.RelayStatsProvider

	dependencyCloseOnce sync.Once
	dependencyCloseErr  error
	// dependencyCloser exists so shutdown ordering can be tested without a real
	// MaxMind database or Pion peer. New installs the production closure.
	dependencyCloser func() error
}

// New creates a new Server with the given configuration.
func New(cfg *config.Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	clientAddress, err := clientaddr.NewResolver(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, err
	}

	metaProvider, geoipCloser, err := buildMetaProvider(cfg, clientAddress)
	if err != nil {
		return nil, err
	}

	locationStore, err := buildLocationStore(cfg)
	if err != nil {
		if geoipCloser != nil {
			_ = geoipCloser.Close()
		}
		return nil, err
	}

	// Reuse one bounded random payload instead of allocating per transfer.
	payloadBuf := make([]byte, 1<<20)
	if _, err := rand.Read(payloadBuf); err != nil {
		log.Printf("Warning: failed to fill payload buffer with random data: %v", err)
	}

	webrtcCfg := webrtc.DefaultConfig()
	webrtcCfg.MaxSessions = cfg.MaxWebRTCSessions
	webrtcCfg.MaxSessionsPerClient = cfg.MaxWebRTCSessionsPerClient
	if len(cfg.TurnServers) > 0 {
		// Public STUN servers do not need credentials and can be installed on the
		// server-side peer immediately. TURN credentials remain short-lived and
		// are issued to clients by /api/turn/credentials.
		var iceServers []pionwebrtc.ICEServer
		for _, iceServer := range cfg.TurnServers {
			lower := strings.ToLower(strings.TrimSpace(iceServer))
			if strings.HasPrefix(lower, "stun:") || strings.HasPrefix(lower, "stuns:") {
				iceServers = append(iceServers, pionwebrtc.ICEServer{URLs: []string{iceServer}})
			}
		}
		webrtcCfg.ICEServers = iceServers
	}
	webrtcManager := webrtc.NewManager(webrtcCfg)

	s := &Server{
		cfg:                   cfg,
		metaProvider:          metaProvider,
		geoipCloser:           geoipCloser,
		locations:             locationStore,
		payloadBuf:            payloadBuf,
		webrtcManager:         webrtcManager,
		clientAddress:         clientAddress,
		transferLimiter:       limits.NewTransferLimiter(cfg.MaxConcurrentTransfers, cfg.MaxConcurrentTransfersPerClient),
		bandwidthQuota:        limits.NewByteQuota(cfg.ClientBandwidthQuotaBytes, cfg.ClientBandwidthQuotaWindow),
		offerRateLimiter:      limits.NewKeyedRateLimiter(float64(cfg.WebRTCOfferRatePerMinute)/60.0, cfg.WebRTCOfferBurst),
		turnCredentialLimiter: limits.NewKeyedRateLimiter(float64(cfg.TurnCredentialRatePerMinute)/60.0, cfg.TurnCredentialBurst),
		metrics:               &serviceMetrics{},
	}
	s.dependencyCloser = func() error {
		if s.webrtcManager != nil {
			s.webrtcManager.Shutdown()
		}
		if s.geoipCloser != nil {
			return s.geoipCloser.Close()
		}
		return nil
	}

	if cfg.TLSEnabled() {
		certificate, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			_ = s.closeDependencies()
			return nil, fmt.Errorf("load TLS certificate/key pair: %w", err)
		}
		s.tlsConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		}
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Static assets and /health remain public. CORS stays outside authentication
	// so a valid preflight never requires a bearer token.
	var handler http.Handler = s.authenticationMiddleware(mux)
	if cfg.EnableCORS {
		handler = s.corsMiddleware(handler)
	}
	// Recovery must receive the tracking writer installed by outer logging so it
	// can distinguish an uncommitted response from a partially written stream.
	handler = s.recoveryMiddleware(handler)
	handler = s.loggingMiddleware(handler)

	s.httpServer = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       cfg.IdleTimeout,
		TLSConfig:         s.tlsConfig,
	}
	return s, nil
}

func buildMetaProvider(cfg *config.Config, resolver *clientaddr.Resolver) (meta.Provider, io.Closer, error) {
	if cfg.GeoIPASNDatabasePath != "" || cfg.GeoIPCityDatabasePath != "" {
		provider, err := meta.NewCityGeoIPProvider(
			cfg.GeoIPASNDatabasePath,
			cfg.GeoIPCityDatabasePath,
			cfg.Hostname,
			cfg.Colo,
			resolver,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("load GeoIP databases: %w", err)
		}
		log.Printf("GeoIP databases loaded: asn=%q city=%q", cfg.GeoIPASNDatabasePath, cfg.GeoIPCityDatabasePath)
		return provider, provider, nil
	}

	// Unknown location data remains unknown. In particular, absence of a City
	// database must not label every client as being in the United States.
	return &meta.StaticProvider{
		Hostname:      cfg.Hostname,
		Colo:          cfg.Colo,
		Country:       "",
		City:          "",
		Region:        "",
		PostalCode:    "",
		Latitude:      0,
		Longitude:     0,
		Timezone:      "",
		ASN:           0,
		ASOrg:         "",
		ClientAddress: resolver,
	}, nil, nil
}

func buildLocationStore(cfg *config.Config) (locations.Store, error) {
	locationsFile := cfg.LocationsFile
	if locationsFile == "" {
		locationsFile = "locations.json"
	}
	if store, err := locations.NewFileStore(locationsFile); err == nil {
		log.Printf("Loaded locations from %s", locationsFile)
		return store, nil
	} else if cfg.LocationsFile != "" {
		return nil, fmt.Errorf("failed to load locations: %w", err)
	}
	log.Printf("Using built-in default locations")
	return locations.NewMemoryStore(locations.DefaultLocations()), nil
}

// registerRoutes sets up all HTTP routes with endpoint-aware deadlines.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	control := func(handler http.HandlerFunc) http.Handler {
		return s.withEndpointDeadline(s.cfg.ControlTimeout, true, true, handler)
	}
	download := func(handler http.HandlerFunc) http.Handler {
		return s.withEndpointDeadline(s.cfg.TransferTimeout, false, true, handler)
	}
	upload := func(handler http.HandlerFunc) http.Handler {
		return s.withEndpointDeadline(s.cfg.TransferTimeout, true, true, handler)
	}

	mux.Handle("/meta", control(s.handleMeta))
	mux.Handle("/__down", download(s.handleDown))
	mux.Handle("/__up", upload(s.handleUp))
	mux.Handle("/locations", control(s.handleLocations))
	mux.Handle("/cdn-cgi/trace", control(s.handleTrace))
	mux.Handle("/api/turn/credentials", control(s.handleTurnCredentials))
	mux.Handle("/api/packet-test/offer", control(s.handlePacketTestOffer))
	mux.Handle("/api/packet-test/report", control(s.handlePacketTestReport))
	mux.Handle("/health", control(s.handleHealth))
	if s.cfg.EnableMetrics {
		mux.Handle("/metrics", control(s.handleMetrics))
	}

	if s.cfg.WebDir != "" {
		files := http.FileServer(http.Dir(s.cfg.WebDir))
		mux.Handle("/", s.withEndpointDeadline(s.cfg.ControlTimeout, true, true, s.staticFileHandler(files)))
	}
}

// staticFileHandler prevents the static fallback from shadowing service routes.
func (s *Server) staticFileHandler(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/meta" || path == "/__down" || path == "/__up" ||
			path == "/locations" || path == "/health" || path == "/metrics" ||
			strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/cdn-cgi/") {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	log.Printf("Starting netspeedd on %s", s.cfg.ListenAddr)
	if s.cfg.WebDir != "" {
		log.Printf("Serving static files from %s", s.cfg.WebDir)
	}

	listenerConfig := DefaultListenerConfig()
	log.Printf("TCP buffers: send=%dKB recv=%dKB nodelay=%v",
		listenerConfig.SendBufSize/1024, listenerConfig.RecvBufSize/1024, listenerConfig.NoDelay)
	listener, err := NewOptimizedListener(s.cfg.ListenAddr, listenerConfig)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	if s.cfg.TLSEnabled() {
		log.Printf("TLS enabled with minimum version TLS 1.2")
		err = s.httpServer.ServeTLS(listener, "", "")
	} else {
		err = s.httpServer.Serve(listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown first stops admission and drains HTTP handlers. Shared WebRTC and
// GeoIP dependencies are closed only after that drain succeeds. If ctx expires,
// dependencies remain open so an in-flight handler can never observe a closed
// resource; the caller may retry with a longer context or use Close to force it.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	return s.closeDependencies()
}

// Close force-closes HTTP connections and then closes dependencies. It is
// intended for listener/startup failure paths, not normal graceful shutdown.
func (s *Server) Close() error {
	var closeErrors []error
	if s.httpServer != nil {
		if err := s.httpServer.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			closeErrors = append(closeErrors, err)
		}
	}
	if err := s.closeDependencies(); err != nil {
		closeErrors = append(closeErrors, err)
	}
	return errors.Join(closeErrors...)
}

func (s *Server) closeDependencies() error {
	s.dependencyCloseOnce.Do(func() {
		if s.dependencyCloser != nil {
			s.dependencyCloseErr = s.dependencyCloser()
		}
	})
	return s.dependencyCloseErr
}

// handleHealth is a simple health check endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// getTLSVersion returns the TLS version string from a request.
func getTLSVersion(r *http.Request) string {
	if r.TLS == nil {
		return "none"
	}
	switch r.TLS.Version {
	case tls.VersionSSL30:
		return "SSLv3"
	case tls.VersionTLS10:
		return "TLSv1.0"
	case tls.VersionTLS11:
		return "TLSv1.1"
	case tls.VersionTLS12:
		return "TLSv1.2"
	case tls.VersionTLS13:
		return "TLSv1.3"
	default:
		return "unknown"
	}
}

// getHTTPVersion returns a cleaned HTTP version string.
func getHTTPVersion(r *http.Request) string {
	proto := strings.ToLower(r.Proto)
	switch proto {
	case "http/1.0":
		return "http/1.0"
	case "http/1.1":
		return "http/1.1"
	case "http/2.0", "http/2":
		return "h2"
	case "http/3.0", "http/3":
		return "h3"
	default:
		return proto
	}
}
