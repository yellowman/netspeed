// Package turn provides an embedded TURN server for packet loss testing.
package turn

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"

	"github.com/pion/turn/v2"
)

// Server wraps a pion TURN server.
type Server struct {
	server     *turn.Server
	listenAddr string
	realm      string
	secret     string
}

// Config holds configuration for the embedded TURN server.
type Config struct {
	// ListenAddr is the UDP address to listen on (e.g., "0.0.0.0:3478")
	ListenAddr string
	// Realm is the TURN realm
	Realm string
	// Secret is the shared secret for credential generation
	// If empty, a random secret will be generated
	Secret string
	// PublicIP is the public IP to advertise (optional)
	PublicIP string
}

// New creates a new embedded TURN server.
func New(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "0.0.0.0:3478"
	}
	if cfg.Realm == "" {
		cfg.Realm = "netspeed"
	}
	if cfg.Secret == "" {
		// Generate a random secret
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return nil, fmt.Errorf("failed to generate secret: %w", err)
		}
		cfg.Secret = hex.EncodeToString(secretBytes)
	}

	// Parse listen address
	udpAddr, err := net.ResolveUDPAddr("udp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	// Create UDP listener
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on UDP: %w", err)
	}

	// Determine relay address
	var relayAddressGenerator turn.RelayAddressGenerator
	if cfg.PublicIP != "" {
		relayAddressGenerator = &turn.RelayAddressGeneratorStatic{
			RelayAddress: net.ParseIP(cfg.PublicIP),
			Address:      "0.0.0.0",
		}
	} else {
		relayAddressGenerator = &turn.RelayAddressGeneratorNone{}
	}

	// Create TURN server with COTURN-style time-limited credentials
	turnServer, err := turn.NewServer(turn.ServerConfig{
		Realm: cfg.Realm,
		AuthHandler: func(username string, realm string, srcAddr net.Addr) ([]byte, bool) {
			// COTURN-style time-limited credentials
			// The credential/password is base64(HMAC-SHA1(secret, username))
			// This matches what the /api/turn/credentials endpoint generates
			mac := hmac.New(sha1.New, []byte(cfg.Secret))
			mac.Write([]byte(username))
			password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
			return turn.GenerateAuthKey(username, realm, password), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn:            conn,
				RelayAddressGenerator: relayAddressGenerator,
			},
		},
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create TURN server: %w", err)
	}

	return &Server{
		server:     turnServer,
		listenAddr: cfg.ListenAddr,
		realm:      cfg.Realm,
		secret:     cfg.Secret,
	}, nil
}

// ListenAddr returns the address the server is listening on.
func (s *Server) ListenAddr() string {
	return s.listenAddr
}

// Realm returns the TURN realm.
func (s *Server) Realm() string {
	return s.realm
}

// Secret returns the shared secret.
func (s *Server) Secret() string {
	return s.secret
}

// Close shuts down the TURN server.
func (s *Server) Close() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// Start logs that the server is running (it starts automatically in New).
func (s *Server) Start() {
	log.Printf("Embedded TURN server listening on %s (realm: %s)", s.listenAddr, s.realm)
}
