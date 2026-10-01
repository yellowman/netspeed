package websocketping

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
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
	return serveWithPolicy(writer, request, nil, nil, onPing)
}

// ServeWithPolicy registers the hijacked connection before writing the upgrade.
// onFrame admits and accounts every incoming frame, including control traffic.
// It receives the total frame bytes for the incoming message and its response.
func ServeWithPolicy(writer http.ResponseWriter, request *http.Request, onConnect func(net.Conn), onFrame func(int64) error) error {
	return serveWithPolicy(writer, request, onConnect, onFrame, nil)
}

func serveWithPolicy(writer http.ResponseWriter, request *http.Request, onConnect func(net.Conn), onFrame func(int64) error, onPing func()) error {
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
	if onConnect != nil {
		onConnect(connection)
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
	return serveConnection(connection, buffered.Reader, buffered.Writer, onFrame, onPing)
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

func serveConnection(connection net.Conn, reader *bufio.Reader, writer *bufio.Writer, onFrame func(int64) error, onPing ...func()) error {
	defer connection.Close()
	for {
		incoming, err := readFrame(reader, true)
		if err != nil {
			return err
		}
		if onFrame != nil {
			// Payload plus masked client and unmasked server frame headers.
			bytes := int64(len(incoming.payload)*2 + 8)
			if len(incoming.payload) > 125 {
				bytes += 4
			}
			if err := onFrame(bytes); err != nil {
				_ = writeClose(writer, 1008, err.Error(), false)
				_ = writer.Flush()
				return err
			}
		}
		switch incoming.opcode {
		case opBinary:
			if err := ValidatePayload(incoming.payload); err != nil {
				_ = writeClose(writer, closeInvalidPayload, err.Error(), false)
				_ = writer.Flush()
				return err
			}
			if len(onPing) > 0 && onPing[0] != nil {
				onPing[0]()
			}
			if err := writeFrame(writer, opBinary, incoming.payload, false); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		case opPing:
			if err := writeFrame(writer, opPong, incoming.payload, false); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
		case opPong:
			continue
		case opClose:
			_ = writeFrame(writer, opClose, incoming.payload, false)
			_ = writer.Flush()
			return nil
		case opText:
			_ = writeClose(writer, closeUnsupportedData, "binary ping required", false)
			_ = writer.Flush()
			return fmt.Errorf("text WebSocket ping is unsupported")
		case opContinuation:
			_ = writeClose(writer, closeProtocolError, "fragmentation unsupported", false)
			_ = writer.Flush()
			return fmt.Errorf("fragmented WebSocket ping is unsupported")
		default:
			_ = writeClose(writer, closeProtocolError, "unsupported opcode", false)
			_ = writer.Flush()
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
