package server

import (
	"net"
	"time"
)

// OptimizedListener wraps a net.Listener to configure TCP options
// for better speed test performance.
type OptimizedListener struct {
	net.Listener
	sendBufSize int
	recvBufSize int
	noDelay     bool
}

// ListenerConfig holds configuration for the optimized listener.
type ListenerConfig struct {
	// SendBufSize is the TCP send buffer size in bytes.
	// Default: 4MB for high-speed connections.
	SendBufSize int

	// RecvBufSize is the TCP receive buffer size in bytes.
	// Default: 4MB for high-speed connections.
	RecvBufSize int

	// NoDelay disables Nagle's algorithm (TCP_NODELAY).
	// This reduces latency for small writes at the cost of
	// potentially more packets. Default: true for speed tests.
	NoDelay bool
}

// DefaultListenerConfig returns sensible defaults for speed testing.
func DefaultListenerConfig() ListenerConfig {
	return ListenerConfig{
		SendBufSize: 4 * 1024 * 1024, // 4 MB
		RecvBufSize: 4 * 1024 * 1024, // 4 MB
		NoDelay:     true,
	}
}

// NewOptimizedListener creates a listener with optimized TCP settings.
func NewOptimizedListener(addr string, cfg ListenerConfig) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	return &OptimizedListener{
		Listener:    ln,
		sendBufSize: cfg.SendBufSize,
		recvBufSize: cfg.RecvBufSize,
		noDelay:     cfg.NoDelay,
	}, nil
}

// Accept waits for and returns the next connection, with optimized settings.
func (l *OptimizedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	// Apply TCP optimizations
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		// Set TCP_NODELAY to disable Nagle's algorithm
		// This is crucial for latency-sensitive speed tests
		if l.noDelay {
			tcpConn.SetNoDelay(true)
		}

		// Increase send buffer for high-throughput downloads
		if l.sendBufSize > 0 {
			tcpConn.SetWriteBuffer(l.sendBufSize)
		}

		// Increase receive buffer for high-throughput uploads
		if l.recvBufSize > 0 {
			tcpConn.SetReadBuffer(l.recvBufSize)
		}

		// Enable TCP keepalive for long-running connections
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	return conn, nil
}
