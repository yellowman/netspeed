// Package server provides the HTTP server and request handling for netspeedd.
package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yellowman/netspeed/internal/config"
	"github.com/yellowman/netspeed/internal/locations"
	"github.com/yellowman/netspeed/internal/meta"
)

// Server is the main netspeedd HTTP server.
type Server struct {
	cfg          *config.Config
	httpServer   *http.Server
	metaProvider meta.Provider
	locations    locations.Store
	payloadBuf   []byte
}

// New creates a new Server with the given configuration.
func New(cfg *config.Config) (*Server, error) {
	// Build meta provider based on configuration
	metaProvider := &meta.StaticProvider{
		Hostname:   cfg.Hostname,
		Colo:       cfg.Colo,
		TrustProxy: cfg.TrustProxyHeaders,
		// Default values - can be configured or looked up via GeoIP
		Country:    "US",
		City:       "Unknown",
		Region:     "Unknown",
		PostalCode: "",
		Latitude:   0,
		Longitude:  0,
		Timezone:   "UTC",
		ASN:        0,
		ASOrg:      "Unknown",
	}

	// Build location store
	var locationStore locations.Store
	if cfg.LocationsFile != "" {
		store, err := locations.NewFileStore(cfg.LocationsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load locations: %w", err)
		}
		locationStore = store
	} else {
		locationStore = locations.NewMemoryStore(locations.DefaultLocations())
	}

	// Allocate payload buffer (1 MiB of random data)
	bufSize := 1 << 20 // 1 MiB
	payloadBuf := make([]byte, bufSize)
	if _, err := rand.Read(payloadBuf); err != nil {
		// Fallback to zeros if random fails
		log.Printf("Warning: failed to fill payload buffer with random data: %v", err)
	}

	s := &Server{
		cfg:          cfg,
		metaProvider: metaProvider,
		locations:    locationStore,
		payloadBuf:   payloadBuf,
	}

	// Set up HTTP mux and routes
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	// Wrap with CORS middleware if enabled
	var handler http.Handler = mux
	if cfg.EnableCORS {
		handler = s.corsMiddleware(handler)
	}

	// Wrap with logging middleware
	handler = s.loggingMiddleware(handler)

	// Wrap with recovery middleware
	handler = s.recoveryMiddleware(handler)

	s.httpServer = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	return s, nil
}

// registerRoutes sets up all HTTP routes.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Core measurement endpoints
	mux.HandleFunc("/meta", s.handleMeta)
	mux.HandleFunc("/__down", s.handleDown)
	mux.HandleFunc("/__up", s.handleUp)
	mux.HandleFunc("/locations", s.handleLocations)

	// Optional diagnostic endpoint
	mux.HandleFunc("/cdn-cgi/trace", s.handleTrace)

	// TURN credentials endpoint
	mux.HandleFunc("/api/turn/credentials", s.handleTurnCredentials)

	// WebRTC packet-test signaling (placeholder - requires pion/webrtc)
	mux.HandleFunc("/api/packet-test/offer", s.handlePacketTestOffer)
	mux.HandleFunc("/api/packet-test/report", s.handlePacketTestReport)

	// Health check
	mux.HandleFunc("/health", s.handleHealth)
}

// Run starts the HTTP server.
func (s *Server) Run() error {
	log.Printf("Starting netspeedd on %s", s.cfg.ListenAddr)

	if s.cfg.TLSEnabled() {
		log.Printf("TLS enabled with cert=%s key=%s", s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		return s.httpServer.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// corsMiddleware handles CORS headers and preflight requests.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check if origin is allowed
		allowed := false
		for _, o := range s.cfg.AllowedOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}

		if !allowed {
			next.ServeHTTP(w, r)
			return
		}

		// Set CORS headers
		if len(s.cfg.AllowedOrigins) == 1 && s.cfg.AllowedOrigins[0] == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		// Handle preflight requests
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware logs HTTP requests.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response wrapper to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		log.Printf("%s %s %d %s %s",
			r.Method,
			r.URL.Path,
			rw.statusCode,
			duration,
			meta.ClientIPFromRequest(r, s.cfg.TrustProxyHeaders),
		)
	})
}

// recoveryMiddleware recovers from panics and logs them.
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Panic recovered: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// setServerTiming adds the Server-Timing header if enabled.
func (s *Server) setServerTiming(w http.ResponseWriter, start time.Time) {
	if s.cfg.EnableServerTiming {
		durMs := time.Since(start).Milliseconds()
		w.Header().Set("Server-Timing", fmt.Sprintf("app;dur=%d", durMs))
	}
}

// setMetaHeaders adds cf-meta-* headers to the response.
func (s *Server) setMetaHeaders(w http.ResponseWriter, clientMeta meta.ClientMeta, requestTime time.Time) {
	w.Header().Set("cf-meta-asn", fmt.Sprintf("%d", clientMeta.ASN))
	w.Header().Set("cf-meta-city", clientMeta.City)
	w.Header().Set("cf-meta-colo", clientMeta.Colo)
	w.Header().Set("cf-meta-country", clientMeta.Country)
	w.Header().Set("cf-meta-ip", clientMeta.ClientIP)
	w.Header().Set("cf-meta-latitude", fmt.Sprintf("%f", clientMeta.Latitude))
	w.Header().Set("cf-meta-longitude", fmt.Sprintf("%f", clientMeta.Longitude))
	w.Header().Set("cf-meta-postalcode", clientMeta.PostalCode)
	w.Header().Set("cf-meta-request-time", fmt.Sprintf("%d", requestTime.UnixMilli()))
	if clientMeta.Timezone != "" {
		w.Header().Set("cf-meta-timezone", clientMeta.Timezone)
	}
}

// handleHealth is a simple health check endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// getTLSVersion returns the TLS version string from a request.
func getTLSVersion(r *http.Request) string {
	if r.TLS == nil {
		return "none"
	}

	switch r.TLS.Version {
	case 0x0300:
		return "SSLv3"
	case 0x0301:
		return "TLSv1.0"
	case 0x0302:
		return "TLSv1.1"
	case 0x0303:
		return "TLSv1.2"
	case 0x0304:
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
