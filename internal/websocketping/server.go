package websocketping

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/yellowman/netspeed/internal/measurementhttp"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Serve upgrades one HTTP/1.1 request and echoes valid Netspeed binary ping
// messages until the peer closes the socket or the endpoint deadline expires.
// Browser clients use an application message because the WebSocket API does not
// expose RFC 6455 control ping frames.
func Serve(writer http.ResponseWriter, request *http.Request, onPing func()) error {
	return serveWithPolicy(writer, request, Policy{}, onPing)
}

// Policy separates incoming-message admission from actual wire-byte accounting.
// ReserveWrite admits an encoded frame and returns a settlement callback for
// actual bytes written. A single policy-close notification bypasses admission,
// but its actual bytes are still charged through OnPolicyClose.
type Policy struct {
	OnConnect     func(net.Conn)
	OnFrame       func() error
	OnRead        func(int64) error
	ReserveWrite  func(int64) (func(int64), error)
	OnPolicyClose func(int64)
}

// ServeWithPolicy registers the hijacked connection before writing the upgrade.
// Accounting begins with frames, not HTTP handshake headers.
func ServeWithPolicy(writer http.ResponseWriter, request *http.Request, policy Policy) error {
	return serveWithPolicy(writer, request, policy, nil)
}

func serveWithPolicy(writer http.ResponseWriter, request *http.Request, policy Policy, onPing func()) error {
	key, err := validateUpgradeRequest(request)
	if err != nil {
		writer.Header().Set("Cache-Control", measurementhttp.CacheControl)
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return err
	}

	connection, buffered, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		http.Error(writer, "WebSocket upgrade is unavailable", http.StatusHTTPVersionNotSupported)
		return fmt.Errorf("hijack WebSocket connection: %w", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = connection.Close()
		}
	}()
	if policy.OnConnect != nil {
		policy.OnConnect(connection)
	}

	accept := websocketAccept(key)
	if _, err := fmt.Fprintf(buffered,
		"HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n"+
			"Sec-WebSocket-Protocol: %s\r\n"+
			"Cache-Control: %s\r\n"+
			"Pragma: no-cache\r\n"+
			"X-Accel-Buffering: no\r\n"+
			"X-Netspeed-Measurement: latency\r\n\r\n",
		accept, measurementhttp.WebSocketPingSubprotocol, measurementhttp.CacheControl); err != nil {
		return fmt.Errorf("write WebSocket upgrade: %w", err)
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("flush WebSocket upgrade: %w", err)
	}
	accepted = true
	return serveConnection(connection, buffered.Reader, policy, onPing)
}

func validateUpgradeRequest(request *http.Request) (string, error) {
	if request.Method != http.MethodGet {
		return "", fmt.Errorf("WebSocket ping requires GET")
	}
	if request.ProtoMajor != 1 {
		return "", fmt.Errorf("WebSocket ping requires an HTTP/1.1 Upgrade")
	}
	if !headerContainsToken(request.Header, "Connection", "upgrade") ||
		!headerContainsToken(request.Header, "Upgrade", "websocket") {
		return "", fmt.Errorf("missing WebSocket Upgrade headers")
	}
	if !headerContainsToken(request.Header, "Sec-WebSocket-Version", "13") {
		return "", fmt.Errorf("unsupported WebSocket version")
	}
	if !headerContainsToken(request.Header, "Sec-WebSocket-Protocol", measurementhttp.WebSocketPingSubprotocol) {
		return "", fmt.Errorf("missing required WebSocket subprotocol %s", measurementhttp.WebSocketPingSubprotocol)
	}
	values := request.Header.Values("Sec-WebSocket-Key")
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return "", fmt.Errorf("invalid Sec-WebSocket-Key")
	}
	key := strings.TrimSpace(values[0])
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 16 {
		return "", fmt.Errorf("invalid Sec-WebSocket-Key")
	}
	return key, nil
}

type accountedReader struct {
	io.Reader
	onRead func(int64) error
	denied error
}

func (reader *accountedReader) Read(buffer []byte) (int, error) {
	n, err := reader.Reader.Read(buffer)
	if reader.onRead != nil && n > 0 {
		if denied := reader.onRead(int64(n)); denied != nil {
			reader.denied = denied
			return n, denied
		}
	}
	return n, err
}

type countedWriter struct {
	io.Writer
	bytes int64
}

func (writer *countedWriter) Write(buffer []byte) (int, error) {
	n, err := writer.Writer.Write(buffer)
	writer.bytes += int64(n)
	return n, err
}

type writePolicyError struct{ error }

func writeEncodedFrame(connection net.Conn, encoded []byte, policy Policy) error {
	var settle func(int64)
	if policy.ReserveWrite != nil {
		var err error
		settle, err = policy.ReserveWrite(int64(len(encoded)))
		if err != nil {
			return writePolicyError{err}
		}
	}
	writer := &countedWriter{Writer: connection}
	defer func() {
		if settle != nil {
			settle(writer.bytes)
		}
	}()
	return writeAll(writer, encoded)
}

func sendFrame(connection net.Conn, opcode byte, payload []byte, policy Policy) error {
	var encoded bytes.Buffer
	if err := writeFrame(&encoded, opcode, payload, false); err != nil {
		return err
	}
	return writeEncodedFrame(connection, encoded.Bytes(), policy)
}

func sendClose(connection net.Conn, code uint16, reason string, policy Policy) error {
	var encoded bytes.Buffer
	if err := writeClose(&encoded, code, reason, false); err != nil {
		return err
	}
	return writeEncodedFrame(connection, encoded.Bytes(), policy)
}

func policyClose(connection net.Conn, err error, policy Policy) {
	// Admission has failed, so one bounded closing notification may exceed the
	// remaining quota. Charge only the bytes actually written, including partial
	// failures; never reserve or assume an unsent symmetric reply.
	writer := &countedWriter{Writer: connection}
	_ = writeClose(writer, 1008, err.Error(), false)
	if policy.OnPolicyClose != nil && writer.bytes > 0 {
		policy.OnPolicyClose(writer.bytes)
	}
}

func serveConnection(connection net.Conn, reader *bufio.Reader, policy Policy, onPing ...func()) error {
	defer connection.Close()
	// Preserve frames prefetched by the HTTP upgrade, then account new network
	// reads below buffering. Even bytes prefetched beyond an invalid header
	// have actually arrived and must not disappear from the quota charge.
	prefetched, err := reader.Peek(reader.Buffered())
	if err != nil {
		return err
	}
	input := &accountedReader{
		Reader: io.MultiReader(bytes.NewReader(bytes.Clone(prefetched)), connection),
		onRead: policy.OnRead,
	}
	reader = bufio.NewReader(input)
	finishWrite := func(err error) error {
		var denied writePolicyError
		if errors.As(err, &denied) {
			policyClose(connection, denied.error, policy)
		}
		return err
	}
	for {
		incoming, err := readFrame(reader, true)
		if input.denied != nil {
			policyClose(connection, input.denied, policy)
			return input.denied
		}
		if err != nil {
			return err
		}
		if policy.OnFrame != nil {
			if err := policy.OnFrame(); err != nil {
				policyClose(connection, err, policy)
				return err
			}
		}
		switch incoming.opcode {
		case opBinary:
			if err := ValidatePayload(incoming.payload); err != nil {
				if closeErr := sendClose(connection, closeInvalidPayload, err.Error(), policy); closeErr != nil {
					return finishWrite(closeErr)
				}
				return err
			}
			if len(onPing) > 0 && onPing[0] != nil {
				onPing[0]()
			}
			if err := sendFrame(connection, opBinary, incoming.payload, policy); err != nil {
				return finishWrite(err)
			}
		case opPing:
			if err := sendFrame(connection, opPong, incoming.payload, policy); err != nil {
				return finishWrite(err)
			}
		case opPong:
			continue
		case opClose:
			return finishWrite(sendFrame(connection, opClose, incoming.payload, policy))
		case opText:
			if err := sendClose(connection, closeUnsupportedData, "binary ping required", policy); err != nil {
				return finishWrite(err)
			}
			return fmt.Errorf("text WebSocket ping is unsupported")
		case opContinuation:
			if err := sendClose(connection, closeProtocolError, "fragmentation unsupported", policy); err != nil {
				return finishWrite(err)
			}
			return fmt.Errorf("fragmented WebSocket ping is unsupported")
		default:
			if err := sendClose(connection, closeProtocolError, "unsupported opcode", policy); err != nil {
				return finishWrite(err)
			}
			return fmt.Errorf("unsupported WebSocket opcode %d", incoming.opcode)
		}
	}
}

func websocketAccept(key string) string {
	digest := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func headerContainsToken(header http.Header, name, target string) bool {
	for _, line := range header.Values(name) {
		for _, raw := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(raw), target) {
				return true
			}
		}
	}
	return false
}

// LogServeError suppresses expected peer-close noise while retaining protocol
// and transport failures useful to operators.
func LogServeError(remote string, err error) {
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
		return
	}
	log.Printf("WebSocket ping ended: client=%s error=%v", remote, err)
}
