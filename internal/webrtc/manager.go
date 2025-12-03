// Package webrtc provides WebRTC peer connection management for packet loss testing.
package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
)

// Manager handles WebRTC peer connections for packet loss testing.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	config   Config
}

// Config holds WebRTC manager configuration.
type Config struct {
	ICEServers     []webrtc.ICEServer
	TestTimeout    time.Duration // How long to keep a test session alive
	CleanupTicker  time.Duration // How often to clean up expired sessions
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		TestTimeout:   30 * time.Second,
		CleanupTicker: 10 * time.Second,
	}
}

// Session represents an active WebRTC test session.
type Session struct {
	ID             string
	PeerConnection *webrtc.PeerConnection
	DataChannel    *webrtc.DataChannel
	CreatedAt      time.Time
	Stats          *SessionStats
	done           chan struct{}
}

// SessionStats tracks packet statistics for a session.
type SessionStats struct {
	mu           sync.Mutex
	TotalRecv    int
	LastSeq      int
	StartTime    time.Time
	LastRecvTime time.Time
}

// PacketMessage is the JSON format for packets sent over the data channel.
type PacketMessage struct {
	Seq    int   `json:"seq"`
	SentAt int64 `json:"sentAt"`
	Size   int   `json:"size"`
}

// AckMessage is the JSON format for acknowledgments.
type AckMessage struct {
	Ack        int   `json:"ack"`
	ReceivedAt int64 `json:"receivedAt"`
	SentAt     int64 `json:"sentAt"`
}

// NewManager creates a new WebRTC manager.
func NewManager(cfg Config) *Manager {
	m := &Manager{
		sessions: make(map[string]*Session),
		config:   cfg,
	}

	// Start cleanup goroutine
	go m.cleanupLoop()

	return m
}

// cleanupLoop periodically removes expired sessions.
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(m.config.CleanupTicker)
	defer ticker.Stop()

	for range ticker.C {
		m.cleanupExpired()
	}
}

// cleanupExpired removes sessions that have exceeded the test timeout.
func (m *Manager) cleanupExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, session := range m.sessions {
		if now.Sub(session.CreatedAt) > m.config.TestTimeout {
			log.Printf("Cleaning up expired session: %s", id)
			session.Close()
			delete(m.sessions, id)
		}
	}
}

// SetICEServers updates the ICE servers configuration.
func (m *Manager) SetICEServers(servers []webrtc.ICEServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.ICEServers = servers
}

// HandleOffer processes an SDP offer and returns an answer.
func (m *Manager) HandleOffer(offerSDP string, testProfile string) (answerSDP string, testID string, err error) {
	// Generate test ID
	testID = uuid.New().String()

	// Create peer connection configuration
	config := webrtc.Configuration{
		ICEServers: m.config.ICEServers,
	}

	// Create a new peer connection
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Create session
	session := &Session{
		ID:             testID,
		PeerConnection: peerConnection,
		CreatedAt:      time.Now(),
		Stats: &SessionStats{
			LastSeq:   -1,
			StartTime: time.Now(),
		},
		done: make(chan struct{}),
	}

	// Set up connection state handler
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Session %s: connection state changed to %s", testID, state.String())
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed ||
			state == webrtc.PeerConnectionStateDisconnected {
			m.CloseSession(testID)
		}
	})

	// Set up ICE connection state handler
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("Session %s: ICE connection state changed to %s", testID, state.String())
	})

	// Set up data channel handler
	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("Session %s: data channel opened: %s", testID, dc.Label())

		if dc.Label() == "packet-loss" {
			session.DataChannel = dc
			m.setupPacketLossChannel(session, dc)
		}
	})

	// Parse the offer
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}

	// Set the remote description
	if err := peerConnection.SetRemoteDescription(offer); err != nil {
		peerConnection.Close()
		return "", "", fmt.Errorf("failed to set remote description: %w", err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		peerConnection.Close()
		return "", "", fmt.Errorf("failed to create answer: %w", err)
	}

	// Set the local description
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		peerConnection.Close()
		return "", "", fmt.Errorf("failed to set local description: %w", err)
	}

	// Wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	select {
	case <-gatherComplete:
		// ICE gathering complete
	case <-time.After(10 * time.Second):
		peerConnection.Close()
		return "", "", fmt.Errorf("ICE gathering timeout")
	}

	// Store the session
	m.mu.Lock()
	m.sessions[testID] = session
	m.mu.Unlock()

	// Return the answer SDP
	return peerConnection.LocalDescription().SDP, testID, nil
}

// setupPacketLossChannel sets up handlers for the packet-loss data channel.
func (m *Manager) setupPacketLossChannel(session *Session, dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		log.Printf("Session %s: packet-loss channel opened", session.ID)
		session.Stats.StartTime = time.Now()
	})

	dc.OnClose(func() {
		log.Printf("Session %s: packet-loss channel closed, received %d packets",
			session.ID, session.Stats.TotalRecv)
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// Parse the packet message
		var pkt PacketMessage
		if err := json.Unmarshal(msg.Data, &pkt); err != nil {
			log.Printf("Session %s: failed to parse packet: %v", session.ID, err)
			return
		}

		log.Printf("Session %s: received packet seq=%d", session.ID, pkt.Seq)

		// Update stats
		session.Stats.mu.Lock()
		session.Stats.TotalRecv++
		session.Stats.LastSeq = pkt.Seq
		session.Stats.LastRecvTime = time.Now()
		session.Stats.mu.Unlock()

		// Send ack with timestamps for RTT calculation
		ack := AckMessage{
			Ack:        pkt.Seq,
			ReceivedAt: time.Now().UnixMilli(),
			SentAt:     pkt.SentAt,
		}
		ackData, err := json.Marshal(ack)
		if err != nil {
			log.Printf("Session %s: failed to marshal ack: %v", session.ID, err)
			return
		}

		log.Printf("Session %s: sending ack for seq=%d", session.ID, pkt.Seq)
		if err := dc.Send(ackData); err != nil {
			log.Printf("Session %s: failed to send ack: %v", session.ID, err)
		}
	})

	dc.OnError(func(err error) {
		log.Printf("Session %s: data channel error: %v", session.ID, err)
	})
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(testID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[testID]
	return session, ok
}

// CloseSession closes and removes a session.
func (m *Manager) CloseSession(testID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[testID]; ok {
		session.Close()
		delete(m.sessions, testID)
	}
}

// Close closes a session and its peer connection.
func (s *Session) Close() {
	select {
	case <-s.done:
		// Already closed
		return
	default:
		close(s.done)
	}

	if s.DataChannel != nil {
		s.DataChannel.Close()
	}
	if s.PeerConnection != nil {
		s.PeerConnection.Close()
	}
}

// GetStats returns the current session statistics.
func (s *Session) GetStats() (totalRecv int, lastSeq int, duration time.Duration) {
	s.Stats.mu.Lock()
	defer s.Stats.mu.Unlock()
	return s.Stats.TotalRecv, s.Stats.LastSeq, time.Since(s.Stats.StartTime)
}

// Shutdown closes the manager and all active sessions.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, session := range m.sessions {
		session.Close()
		delete(m.sessions, id)
	}
}
