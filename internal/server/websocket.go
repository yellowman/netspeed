package server

import (
	"net"
	"net/http"

	"github.com/yellowman/netspeed/internal/limits"
)

type webSocketSession struct {
	connection net.Conn
	done       chan struct{}
}

// Register before hijacking so shutdown also owns an upgrade in progress.
func (s *Server) beginWebSocketSession(w http.ResponseWriter) (*webSocketSession, bool) {
	s.webSocketMu.Lock()
	defer s.webSocketMu.Unlock()
	if s.webSocketClosing {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return nil, false
	}
	if s.webSocketSessions == nil {
		s.webSocketSessions = make(map[*webSocketSession]struct{})
	}
	if s.webSocketRateLimiter == nil {
		s.webSocketRateLimiter = limits.NewKeyedRateLimiter(100, 100)
	}
	session := &webSocketSession{done: make(chan struct{})}
	s.webSocketSessions[session] = struct{}{}
	return session, true
}

func (s *Server) attachWebSocket(session *webSocketSession, connection net.Conn) {
	s.webSocketMu.Lock()
	defer s.webSocketMu.Unlock()
	session.connection = connection
	if s.webSocketClosing {
		_ = connection.Close()
	}
}

func (s *Server) finishWebSocket(session *webSocketSession) {
	s.webSocketMu.Lock()
	defer s.webSocketMu.Unlock()
	delete(s.webSocketSessions, session)
	close(session.done)
}

func (s *Server) stopWebSockets() []<-chan struct{} {
	s.webSocketMu.Lock()
	defer s.webSocketMu.Unlock()
	s.webSocketClosing = true
	var sessions []<-chan struct{}
	for session := range s.webSocketSessions {
		if session.connection != nil {
			_ = session.connection.Close()
		}
		sessions = append(sessions, session.done)
	}
	return sessions
}
