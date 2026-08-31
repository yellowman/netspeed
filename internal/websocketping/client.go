package websocketping

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yellowman/netspeed/internal/measurementhttp"
)

// Client is one persistent WebSocket latency connection. Ping serializes only
// the tiny message exchange; time waiting for that lock is not included in RTT.
type Client struct {
	connection net.Conn
	reader     *bufio.Reader
	writer     *bufio.Writer
	timeout    time.Duration
	mutex      sync.Mutex
	closed     bool
}

// Dial establishes and validates an RFC 6455 HTTP/1.1 upgrade. TCP/TLS and
// upgrade time are intentionally outside every measured Ping interval.
func Dial(ctx context.Context, serverURL, endpointPath, accessToken, userAgent string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid WebSocket server URL %q", serverURL)
	}
	endpoint, err := url.Parse(endpointPath)
	if err != nil || endpoint.IsAbs() || endpoint.Host != "" || !strings.HasPrefix(endpoint.Path, "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("invalid WebSocket endpoint path %q", endpointPath)
	}
	requestURL, err := url.Parse(strings.TrimRight(base.String(), "/") + endpoint.Path)
	if err != nil {
		return nil, fmt.Errorf("build WebSocket endpoint URL: %w", err)
	}

	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	address := requestURL.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		port := "80"
		if requestURL.Scheme == "https" {
			port = "443"
		}
		address = net.JoinHostPort(requestURL.Hostname(), port)
	}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("dial WebSocket endpoint: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = connection.Close()
		}
	}()

	if requestURL.Scheme == "https" {
		tlsConnection := tls.Client(connection, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: requestURL.Hostname(),
		})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("WebSocket TLS handshake: %w", err)
		}
		connection = tlsConnection
	}
	if err := setDeadline(connection, ctx, timeout); err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("generate WebSocket handshake key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	httpURL := *requestURL
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", key)
	request.Header.Set("Sec-WebSocket-Protocol", measurementhttp.WebSocketPingSubprotocol)
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", measurementhttp.CacheControl)
	request.Header.Set("Pragma", "no-cache")
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}

	writer := bufio.NewWriter(connection)
	if err := request.Write(writer); err != nil {
		return nil, fmt.Errorf("write WebSocket upgrade request: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return nil, fmt.Errorf("flush WebSocket upgrade request: %w", err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, fmt.Errorf("read WebSocket upgrade response: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = response.Body.Close()
		return nil, fmt.Errorf("WebSocket upgrade returned HTTP %d%s", response.StatusCode, formatDetail(detail))
	}
	if !headerContainsToken(response.Header, "Upgrade", "websocket") || !headerContainsToken(response.Header, "Connection", "upgrade") {
		return nil, errors.New("WebSocket upgrade response omitted required Upgrade headers")
	}
	acceptValues := response.Header.Values("Sec-WebSocket-Accept")
	if len(acceptValues) != 1 || strings.TrimSpace(acceptValues[0]) != websocketAccept(key) {
		return nil, errors.New("WebSocket upgrade returned invalid Sec-WebSocket-Accept")
	}
	protocolValues := response.Header.Values("Sec-WebSocket-Protocol")
	if len(protocolValues) != 1 || strings.TrimSpace(protocolValues[0]) != measurementhttp.WebSocketPingSubprotocol {
		return nil, fmt.Errorf("WebSocket upgrade did not select %s", measurementhttp.WebSocketPingSubprotocol)
	}
	if err := measurementhttp.ValidateIdentityResponseEncoding(response.Header); err != nil {
		return nil, err
	}
	cacheControl := strings.Join(response.Header.Values("Cache-Control"), ",")
	if !hasDirective(cacheControl, "no-store") || !hasDirective(cacheControl, "no-transform") {
		return nil, fmt.Errorf("WebSocket upgrade Cache-Control %q does not preserve no-store, no-transform", cacheControl)
	}
	proxyBuffering, present, err := measurementhttp.UniqueHeaderValue(response.Header, "X-Accel-Buffering")
	if err != nil {
		return nil, err
	}
	if !present || !strings.EqualFold(proxyBuffering, "no") {
		return nil, fmt.Errorf("WebSocket upgrade did not suppress reverse-proxy buffering")
	}
	measurement, present, err := measurementhttp.UniqueHeaderValue(response.Header, "X-Netspeed-Measurement")
	if err != nil {
		return nil, err
	}
	if !present || !strings.EqualFold(measurement, "latency") {
		return nil, fmt.Errorf("WebSocket upgrade did not identify a latency measurement")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear WebSocket handshake deadline: %w", err)
	}
	closeOnError = false
	return &Client{connection: connection, reader: reader, writer: writer, timeout: timeout}, nil
}

// Ping sends one fixed binary nonce and accepts only its exact echo. The
// persistent connection and all upgrade work predate start, so the returned
// duration contains only the application message round trip.
func (client *Client) Ping(ctx context.Context, payload []byte) (time.Duration, error) {
	if err := ValidatePayload(payload); err != nil {
		return 0, err
	}
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed || client.connection == nil {
		return 0, errors.New("WebSocket ping connection is closed")
	}
	if err := setDeadline(client.connection, ctx, client.timeout); err != nil {
		return 0, err
	}
	defer client.connection.SetDeadline(time.Time{}) // best-effort cleanup

	started := time.Now()
	if err := writeFrame(client.writer, opBinary, payload, true); err != nil {
		return 0, fmt.Errorf("send WebSocket ping: %w", err)
	}
	if err := client.writer.Flush(); err != nil {
		return 0, fmt.Errorf("flush WebSocket ping: %w", err)
	}
	for {
		incoming, err := readFrame(client.reader, false)
		if err != nil {
			return 0, fmt.Errorf("receive WebSocket ping echo: %w", err)
		}
		switch incoming.opcode {
		case opBinary:
			if len(incoming.payload) != len(payload) || !equalBytes(incoming.payload, payload) {
				return 0, errors.New("WebSocket ping echo did not match the transmitted nonce")
			}
			duration := time.Since(started)
			if duration <= 0 {
				return 0, errors.New("WebSocket ping produced non-positive latency")
			}
			return duration, nil
		case opPing:
			if err := writeFrame(client.writer, opPong, incoming.payload, true); err != nil {
				return 0, err
			}
			if err := client.writer.Flush(); err != nil {
				return 0, err
			}
		case opPong:
			continue
		case opClose:
			return 0, errors.New("WebSocket ping peer closed the connection")
		default:
			return 0, fmt.Errorf("WebSocket ping received unsupported opcode %d", incoming.opcode)
		}
	}
}

// Close performs a best-effort normal close and releases the socket.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closed {
		return nil
	}
	client.closed = true
	if client.connection == nil {
		return nil
	}
	_ = client.connection.SetDeadline(time.Now().Add(time.Second))
	_ = writeClose(client.writer, closeNormal, "", true)
	_ = client.writer.Flush()
	return client.connection.Close()
}

func setDeadline(connection net.Conn, ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set WebSocket deadline: %w", err)
	}
	return nil
}

func formatDetail(body []byte) string {
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return ""
	}
	return ": " + detail
}

func hasDirective(value, target string) bool {
	for _, raw := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(raw), target) {
			return true
		}
	}
	return false
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
