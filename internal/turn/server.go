// Package turn provides an embedded TURN server for packet loss testing.
package turn

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pionturn "github.com/pion/turn/v2"

	"github.com/yellowman/netspeed/internal/telemetry"
)

var errRelayRateLimited = errors.New("embedded TURN UDP rate limit exceeded")

// Server wraps a Pion TURN server.
type Server struct {
	server     *pionturn.Server
	listenAddr string
	realm      string
	secret     string
	packetConn *meteredPacketConn
	closeOnce  sync.Once
	closeErr   error
}

// Config holds configuration for the embedded TURN server.
type Config struct {
	// ListenAddr is the UDP address to listen on.
	ListenAddr string
	// Realm is the TURN realm.
	Realm string
	// Secret is the shared secret for credential generation. If empty, a random
	// 256-bit secret is generated for this process lifetime.
	Secret string
	// PublicIP is the IP advertised for relay candidates. It is mandatory for a
	// non-loopback listen address.
	PublicIP string
	// MaxMbps caps combined inbound and outbound bytes at the UDP socket.
	MaxMbps int64
	// MaxCredentialTTL rejects otherwise valid usernames too far in the future.
	MaxCredentialTTL int64
}

// New creates a new embedded TURN server.
func New(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:3478"
	}
	if cfg.Realm == "" {
		cfg.Realm = "netspeed"
	}
	if cfg.MaxMbps <= 0 {
		return nil, fmt.Errorf("TURN UDP rate limit must be positive")
	}
	if cfg.MaxCredentialTTL < 60 {
		cfg.MaxCredentialTTL = 600
	}
	if cfg.Secret == "" {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return nil, fmt.Errorf("generate TURN secret: %w", err)
		}
		cfg.Secret = hex.EncodeToString(secretBytes)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve TURN UDP address: %w", err)
	}
	if udpAddr.IP == nil {
		return nil, fmt.Errorf("TURN listen host must be an IP address")
	}

	relayIP := net.ParseIP(strings.Trim(cfg.PublicIP, "[]"))
	if relayIP == nil {
		if udpAddr.IP.IsLoopback() {
			relayIP = udpAddr.IP
		} else {
			return nil, fmt.Errorf("non-loopback TURN listener requires a public IP")
		}
	}

	connection, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on TURN UDP address: %w", err)
	}
	metered := newMeteredPacketConn(connection, cfg.MaxMbps)

	relayBindAddress := "0.0.0.0"
	if relayIP.To4() == nil {
		relayBindAddress = "::"
	}
	relayAddressGenerator := &pionturn.RelayAddressGeneratorStatic{
		RelayAddress: relayIP,
		Address:      relayBindAddress,
	}

	turnServer, err := pionturn.NewServer(pionturn.ServerConfig{
		Realm: cfg.Realm,
		AuthHandler: func(username, realm string, _ net.Addr) ([]byte, bool) {
			if realm != cfg.Realm {
				return nil, false
			}
			parts := strings.SplitN(username, ":", 2)
			if len(parts) != 2 {
				return nil, false
			}
			expiry, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return nil, false
			}
			now := time.Now().Unix()
			if expiry <= now || expiry > now+cfg.MaxCredentialTTL {
				return nil, false
			}

			mac := hmac.New(sha1.New, []byte(cfg.Secret))
			_, _ = mac.Write([]byte(username))
			password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
			return pionturn.GenerateAuthKey(username, realm, password), true
		},
		PacketConnConfigs: []pionturn.PacketConnConfig{
			{
				PacketConn:            metered,
				RelayAddressGenerator: relayAddressGenerator,
			},
		},
	})
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("create TURN server: %w", err)
	}

	return &Server{
		server:     turnServer,
		listenAddr: connection.LocalAddr().String(),
		realm:      cfg.Realm,
		secret:     cfg.Secret,
		packetConn: metered,
	}, nil
}

// ListenAddr returns the actual address, including a dynamically selected port.
func (server *Server) ListenAddr() string { return server.listenAddr }

// Realm returns the TURN realm.
func (server *Server) Realm() string { return server.realm }

// Secret returns the shared secret.
func (server *Server) Secret() string { return server.secret }

// RelayStats returns lock-free UDP accounting for /metrics.
func (server *Server) RelayStats() telemetry.RelayStats {
	if server.packetConn == nil {
		return telemetry.RelayStats{}
	}
	return server.packetConn.stats()
}

// Close shuts down the TURN server.
func (server *Server) Close() error {
	server.closeOnce.Do(func() {
		if server.server != nil {
			server.closeErr = server.server.Close()
		}
	})
	return server.closeErr
}

// Start is retained for the daemon's startup log; Pion starts in New.
func (server *Server) Start() {}

type byteTokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newByteTokenBucket(maxMbps int64) *byteTokenBucket {
	return newByteTokenBucketWithClock(maxMbps, time.Now)
}

func newByteTokenBucketWithClock(maxMbps int64, now func() time.Time) *byteTokenBucket {
	bytesPerSecond := float64(maxMbps) * 1_000_000 / 8
	return &byteTokenBucket{
		rate:   bytesPerSecond,
		burst:  bytesPerSecond,
		tokens: bytesPerSecond,
		last:   now(),
		now:    now,
	}
}

func (bucket *byteTokenBucket) allow(bytes int) bool {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	now := bucket.now()
	bucket.tokens += now.Sub(bucket.last).Seconds() * bucket.rate
	if bucket.tokens > bucket.burst {
		bucket.tokens = bucket.burst
	}
	bucket.last = now
	if float64(bytes) > bucket.tokens {
		return false
	}
	bucket.tokens -= float64(bytes)
	return true
}

type meteredPacketConn struct {
	connection net.PacketConn
	limiter    *byteTokenBucket

	bytesRead          atomic.Uint64
	bytesWritten       atomic.Uint64
	packetsRead        atomic.Uint64
	packetsWritten     atomic.Uint64
	droppedReadBytes   atomic.Uint64
	rejectedWriteBytes atomic.Uint64
}

func newMeteredPacketConn(connection net.PacketConn, maxMbps int64) *meteredPacketConn {
	return &meteredPacketConn{connection: connection, limiter: newByteTokenBucket(maxMbps)}
}

func (connection *meteredPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	for {
		n, address, err := connection.connection.ReadFrom(buffer)
		if err != nil {
			return n, address, err
		}
		if !connection.limiter.allow(n) {
			connection.droppedReadBytes.Add(uint64(n))
			continue
		}
		connection.bytesRead.Add(uint64(n))
		connection.packetsRead.Add(1)
		return n, address, nil
	}
}

func (connection *meteredPacketConn) WriteTo(buffer []byte, address net.Addr) (int, error) {
	if !connection.limiter.allow(len(buffer)) {
		connection.rejectedWriteBytes.Add(uint64(len(buffer)))
		return 0, errRelayRateLimited
	}
	n, err := connection.connection.WriteTo(buffer, address)
	if n > 0 {
		connection.bytesWritten.Add(uint64(n))
		connection.packetsWritten.Add(1)
	}
	return n, err
}

func (connection *meteredPacketConn) Close() error        { return connection.connection.Close() }
func (connection *meteredPacketConn) LocalAddr() net.Addr { return connection.connection.LocalAddr() }
func (connection *meteredPacketConn) SetDeadline(t time.Time) error {
	return connection.connection.SetDeadline(t)
}
func (connection *meteredPacketConn) SetReadDeadline(t time.Time) error {
	return connection.connection.SetReadDeadline(t)
}
func (connection *meteredPacketConn) SetWriteDeadline(t time.Time) error {
	return connection.connection.SetWriteDeadline(t)
}

func (connection *meteredPacketConn) stats() telemetry.RelayStats {
	return telemetry.RelayStats{
		BytesRead:          connection.bytesRead.Load(),
		BytesWritten:       connection.bytesWritten.Load(),
		PacketsRead:        connection.packetsRead.Load(),
		PacketsWritten:     connection.packetsWritten.Load(),
		DroppedReadBytes:   connection.droppedReadBytes.Load(),
		RejectedWriteBytes: connection.rejectedWriteBytes.Load(),
	}
}
