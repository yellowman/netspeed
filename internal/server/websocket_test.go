package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/clientaddr"
	"github.com/yellowman/netspeed/internal/limits"
	"github.com/yellowman/netspeed/internal/websocketping"
)

func TestWebSocketSameOriginIncludesSchemeAndEffectivePort(t *testing.T) {
	s := measurementTestServer(1024)
	s.cfg.EnableCORS = false
	s.clientAddress, _ = clientaddr.NewResolver([]string{"127.0.0.0/8"})
	for _, test := range []struct {
		origin, host, peer, proto, forwardedHost string
		tls, allowed                             bool
	}{
		{"http://speed.test:80", "speed.test", "192.0.2.1:1", "", "", false, true},
		{"https://speed.test", "speed.test", "192.0.2.1:1", "", "", false, false},
		{"http://speed.test", "speed.test", "192.0.2.1:1", "", "", true, false},
		{"https://speed.test:443", "speed.test", "192.0.2.1:1", "", "", true, true},
		{"https://speed.test:444", "speed.test", "192.0.2.1:1", "", "", true, false},
		{"https://speed.test", "internal:8080", "127.0.0.1:1", "https", "speed.test", false, true},
		{"https://speed.test", "speed.test", "192.0.2.1:1", "https", "", false, false},
		{"https://speed.test", "speed.test", "127.0.0.1:1", "https, http", "", false, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/__ws", nil)
		r.RemoteAddr = test.peer
		r.Header.Set("Origin", test.origin)
		if test.proto != "" {
			r.Header.Set("X-Forwarded-Proto", test.proto)
		}
		if test.forwardedHost != "" {
			r.Header.Set("X-Forwarded-Host", test.forwardedHost)
		}
		if test.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := s.webSocketOriginAllowed(r); got != test.allowed {
			t.Errorf("%+v: allowed=%v", test, got)
		}
	}
}

func TestShutdownDrainsHijackedWebSocketsBeforeDependencies(t *testing.T) {
	s := measurementTestServer(1024)
	closed := false
	s.dependencyCloser = func() error {
		s.webSocketMu.Lock()
		defer s.webSocketMu.Unlock()
		if len(s.webSocketSessions) != 0 || s.metrics.activeTransfers.Load() != 0 {
			t.Error("dependencies closed with live WebSocket handlers")
		}
		closed = true
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(s.handleWebSocketPing))
	defer server.Close()
	s.httpServer = server.Config
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := websocketping.Dial(ctx, server.URL, "/__ws", "", "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("dependencies not closed")
	}
	payload, _ := websocketping.NewPayload(1)
	if _, err := client.Ping(ctx, payload); err == nil {
		t.Fatal("WebSocket survived shutdown")
	}
}

func TestShutdownOwnsUpgradeInProgressAndHonorsTimeout(t *testing.T) {
	s := measurementTestServer(1024)
	closed := false
	s.dependencyCloser = func() error { closed = true; return nil }
	session, ok := s.beginWebSocketSession(httptest.NewRecorder())
	if !ok {
		t.Fatal("registration failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); err == nil || closed {
		t.Fatal("shutdown skipped live upgrade")
	}
	a, b := net.Pipe()
	defer b.Close()
	s.attachWebSocket(session, a)
	if _, err := b.Write([]byte{1}); err == nil {
		t.Fatal("late upgrade connection survived shutdown")
	}
	s.finishWebSocket(session)
	if err := s.Shutdown(context.Background()); err != nil || !closed {
		t.Fatal("shutdown retry failed")
	}
	if _, ok := s.beginWebSocketSession(httptest.NewRecorder()); ok {
		t.Fatal("admitted upgrade after shutdown")
	}
}

func TestWebSocketChargesQuotaAndRateLimitsAcrossSessions(t *testing.T) {
	for _, quota := range []bool{false, true} {
		s := measurementTestServer(1024)
		s.webSocketRateLimiter = limits.NewKeyedRateLimiter(.001, 1)
		if quota {
			s.bandwidthQuota = limits.NewByteQuota(39, time.Minute)
		}
		server := httptest.NewServer(http.HandlerFunc(s.handleWebSocketPing))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		for index := 0; index < 2; index++ {
			client, err := websocketping.Dial(ctx, server.URL, "/__ws", "", "test", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := websocketping.NewPayload(uint32(index))
			_, err = client.Ping(ctx, payload)
			if (quota || index == 1) != (err != nil) {
				t.Errorf("quota=%v session=%d error=%v", quota, index, err)
			}
			_ = client.Close()
		}
		cancel()
		server.Close()
	}
}
