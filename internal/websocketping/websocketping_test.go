package websocketping

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientAndServerPersistentEcho(t *testing.T) {
	var pings atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/__ws" {
			http.NotFound(writer, request)
			return
		}
		if got := request.Header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("Accept-Encoding = %q; want identity", got)
		}
		if got := request.Header.Get("Cache-Control"); got != "no-store, no-transform" {
			t.Errorf("Cache-Control = %q; want no-store, no-transform", got)
		}
		if err := Serve(writer, request, func() { pings.Add(1) }); err != nil && !strings.Contains(strings.ToLower(err.Error()), "eof") {
			t.Errorf("Serve: %v", err)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, server.URL, "/__ws", "", "netspeed-test", time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	for sequence := uint32(1); sequence <= 3; sequence++ {
		payload, err := NewPayload(sequence)
		if err != nil {
			t.Fatalf("NewPayload: %v", err)
		}
		duration, err := client.Ping(ctx, payload)
		if err != nil {
			t.Fatalf("Ping %d: %v", sequence, err)
		}
		if duration <= 0 {
			t.Fatalf("Ping %d duration = %s", sequence, duration)
		}
	}
	if got := pings.Load(); got != 3 {
		t.Fatalf("server observed %d pings; want 3", got)
	}
}

func TestClientRejectsConflictingUpgradeResponseHeaders(t *testing.T) {
	tests := []struct {
		name        string
		headers     string
		errorNeedle string
	}{
		{
			name:        "later content encoding",
			headers:     "Content-Encoding: identity\r\nContent-Encoding: gzip\r\nX-Accel-Buffering: no\r\n",
			errorNeedle: "Content-Encoding",
		},
		{
			name:        "conflicting proxy buffering",
			headers:     "X-Accel-Buffering: no\r\nX-Accel-Buffering: yes\r\n",
			errorNeedle: "X-Accel-Buffering",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				connection, buffered, err := http.NewResponseController(writer).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				defer connection.Close()
				key := strings.TrimSpace(request.Header.Get("Sec-WebSocket-Key"))
				_, _ = fmt.Fprintf(buffered,
					"HTTP/1.1 101 Switching Protocols\r\n"+
						"Upgrade: websocket\r\n"+
						"Connection: Upgrade\r\n"+
						"Sec-WebSocket-Accept: %s\r\n"+
						"Sec-WebSocket-Protocol: netspeed.ping.v1\r\n"+
						"Cache-Control: no-store, no-transform\r\n"+
						"X-Netspeed-Measurement: latency\r\n%s\r\n",
					websocketAccept(key), test.headers)
				_ = buffered.Flush()
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := Dial(ctx, server.URL, "/__ws", "", "netspeed-test", time.Second)
			if client != nil {
				_ = client.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.errorNeedle) {
				t.Fatalf("Dial error = %v; want %s rejection", err, test.errorNeedle)
			}
		})
	}
}

func TestServerRejectsMissingSubprotocol(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/__ws", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "MDEyMzQ1Njc4OWFiY2RlZg==")
	response := httptest.NewRecorder()
	if err := Serve(response, request, nil); err == nil || !strings.Contains(err.Error(), "subprotocol") {
		t.Fatalf("Serve error = %v; want subprotocol rejection", err)
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", response.Code, http.StatusBadRequest)
	}
}

func TestValidatePayloadRejectsWrongMagicAndSize(t *testing.T) {
	if err := ValidatePayload([]byte("short")); err == nil {
		t.Fatal("ValidatePayload accepted short payload")
	}
	payload, err := NewPayload(7)
	if err != nil {
		t.Fatal(err)
	}
	payload[0] = 'X'
	if err := ValidatePayload(payload); err == nil {
		t.Fatal("ValidatePayload accepted bad magic")
	}
}
