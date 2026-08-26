// Package webrtc provides WebRTC peer connection management for packet loss testing.
package webrtc

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	pion "github.com/pion/webrtc/v3"

	"github.com/yellowman/netspeed/internal/protocol"
)

var (
	// ErrManagerClosed is returned when signaling races with manager shutdown.
	ErrManagerClosed = errors.New("WebRTC manager is shut down")
	// ErrSessionClosed is returned when a peer fails or closes during signaling.
	ErrSessionClosed = errors.New("WebRTC session closed")
	// ErrSessionCapacity is returned when the global active-session ceiling is full.
	ErrSessionCapacity = errors.New("WebRTC session capacity reached")
	// ErrClientSessionCapacity is returned when one client reaches its active-session ceiling.
	ErrClientSessionCapacity = errors.New("WebRTC per-client session capacity reached")
)

// SessionState is the manager-owned lifecycle state of a packet-test session.
type SessionState uint8

const (
	SessionStateNew SessionState = iota
	SessionStateNegotiating
	SessionStateConnecting
	SessionStateConnected
	SessionStateDisconnected
	SessionStateClosing
	SessionStateClosed
)

func (state SessionState) String() string {
	switch state {
	case SessionStateNew:
		return "new"
	case SessionStateNegotiating:
		return "negotiating"
	case SessionStateConnecting:
		return "connecting"
	case SessionStateConnected:
		return "connected"
	case SessionStateDisconnected:
		return "disconnected"
	case SessionStateClosing:
		return "closing"
	case SessionStateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Config holds WebRTC manager configuration.
type Config struct {
	ICEServers           []pion.ICEServer
	IdleTimeout          time.Duration // Close session if no packets or state activity occurs for this duration.
	MaxSessionTime       time.Duration // Maximum session lifetime (safety net).
	CleanupTicker        time.Duration // How often to scan for expired sessions.
	DisconnectGrace      time.Duration // Time a transient disconnected state may recover.
	ICEGatherTimeout     time.Duration // Maximum time to wait for local ICE gathering.
	MaxSessions          int           // Global active-session ceiling.
	MaxSessionsPerClient int           // Active-session ceiling for one client identity.
}

// DefaultConfig returns a default configuration.
func DefaultConfig() Config {
	return Config{
		IdleTimeout:          30 * time.Second,
		MaxSessionTime:       120 * time.Second,
		CleanupTicker:        10 * time.Second,
		DisconnectGrace:      5 * time.Second,
		ICEGatherTimeout:     10 * time.Second,
		MaxSessions:          64,
		MaxSessionsPerClient: 2,
	}
}

func normalizeConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.MaxSessionTime <= 0 {
		config.MaxSessionTime = defaults.MaxSessionTime
	}
	if config.CleanupTicker <= 0 {
		config.CleanupTicker = defaults.CleanupTicker
	}
	if config.DisconnectGrace <= 0 {
		config.DisconnectGrace = defaults.DisconnectGrace
	}
	if config.ICEGatherTimeout <= 0 {
		config.ICEGatherTimeout = defaults.ICEGatherTimeout
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = defaults.MaxSessions
	}
	if config.MaxSessionsPerClient <= 0 {
		config.MaxSessionsPerClient = defaults.MaxSessionsPerClient
	}
	config.ICEServers = cloneICEServers(config.ICEServers)
	return config
}

func cloneICEServers(servers []pion.ICEServer) []pion.ICEServer {
	if len(servers) == 0 {
		return nil
	}
	cloned := make([]pion.ICEServer, len(servers))
	for index := range servers {
		cloned[index] = servers[index]
		cloned[index].URLs = append([]string(nil), servers[index].URLs...)
	}
	return cloned
}

func randomSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate WebRTC session ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

type stoppableTimer interface {
	Stop() bool
}

type managerDependencies struct {
	factory   peerConnectionFactory
	now       func() time.Time
	afterFunc func(time.Duration, func()) stoppableTimer
	newID     func() (string, error)
}

// Manager handles WebRTC peer connections for packet loss testing. The manager
// is the sole owner of session removal and resource closure.
type Manager struct {
	mu              sync.RWMutex
	sessions        map[string]*Session
	pendingOffers   int
	pendingByClient map[string]int
	config          Config
	closed          bool

	factory   peerConnectionFactory
	now       func() time.Time
	afterFunc func(time.Duration, func()) stoppableTimer
	newID     func() (string, error)

	ctx          context.Context
	cancel       context.CancelFunc
	cleanupWG    sync.WaitGroup
	offerWG      sync.WaitGroup
	sessionWG    sync.WaitGroup
	shutdownOnce sync.Once
}

// Session represents an active WebRTC test session. Mutable lifecycle fields
// are private so that Manager remains the only resource owner.
type Session struct {
	id          string
	testProfile string
	clientKey   string
	createdAt   time.Time

	peerConnection peerConnection
	now            func() time.Time
	ctx            context.Context
	cancel         context.CancelFunc

	mu                   sync.RWMutex
	state                SessionState
	lastActivity         time.Time
	dataChannel          dataChannel
	dataChannelClaimed   bool
	disconnectTimer      stoppableTimer
	disconnectGeneration uint64
	closeReason          string
	closedAt             time.Time
	onClosed             func()
	closeOnce            sync.Once
	done                 chan struct{}
	stats                SessionStats
}

// SessionStats tracks packet statistics for a session.
type SessionStats struct {
	mu                   sync.RWMutex
	TotalRecv            int
	LastSeq              int
	StartTime            time.Time
	LastRecvTime         time.Time
	ReceivedSequences    map[uint32]struct{}
	DuplicateFrames      int
	InvalidFrames        int
	AcknowledgementsSent int
	AckSendFailures      int
}

// SessionSnapshot is an immutable lifecycle view useful for diagnostics and tests.
type SessionSnapshot struct {
	ID           string
	TestProfile  string
	ClientKey    string
	State        SessionState
	CreatedAt    time.Time
	LastActivity time.Time
	ClosedAt     time.Time
	CloseReason  string
}

// PacketLossSnapshot is the server-observed half of a packet-loss test. The
// client combines it with its acknowledgement set to distinguish forward loss
// from reverse acknowledgement loss and round-trip transaction loss.
type PacketLossSnapshot struct {
	FrameSizeBytes       int
	ForwardReceived      int
	AcknowledgementsSent int
	DuplicateFrames      int
	InvalidFrames        int
	AckSendFailures      int
}

// NewManager creates a new WebRTC manager.
func NewManager(config Config) *Manager {
	return newManager(config, managerDependencies{
		factory: pionPeerConnectionFactory{},
		now:     time.Now,
		afterFunc: func(duration time.Duration, callback func()) stoppableTimer {
			return time.AfterFunc(duration, callback)
		},
		newID: randomSessionID,
	})
}

func newManager(config Config, dependencies managerDependencies) *Manager {
	config = normalizeConfig(config)
	if dependencies.factory == nil {
		dependencies.factory = pionPeerConnectionFactory{}
	}
	if dependencies.now == nil {
		dependencies.now = time.Now
	}
	if dependencies.afterFunc == nil {
		dependencies.afterFunc = func(duration time.Duration, callback func()) stoppableTimer {
			return time.AfterFunc(duration, callback)
		}
	}
	if dependencies.newID == nil {
		dependencies.newID = randomSessionID
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		sessions:        make(map[string]*Session),
		pendingByClient: make(map[string]int),
		config:          config,
		factory:         dependencies.factory,
		now:             dependencies.now,
		afterFunc:       dependencies.afterFunc,
		newID:           dependencies.newID,
		ctx:             ctx,
		cancel:          cancel,
	}

	manager.cleanupWG.Add(1)
	go manager.cleanupLoop(config.CleanupTicker)
	return manager
}

func newSession(managerContext context.Context, id, testProfile string, connection peerConnection, now func() time.Time) *Session {
	createdAt := now()
	ctx, cancel := context.WithCancel(managerContext)
	return &Session{
		id:             id,
		testProfile:    testProfile,
		createdAt:      createdAt,
		peerConnection: connection,
		now:            now,
		ctx:            ctx,
		cancel:         cancel,
		state:          SessionStateNew,
		lastActivity:   createdAt,
		done:           make(chan struct{}),
		stats: SessionStats{
			LastSeq:           -1,
			StartTime:         createdAt,
			ReceivedSequences: make(map[uint32]struct{}),
		},
	}
}

// cleanupLoop periodically removes expired sessions and exits on shutdown.
func (manager *Manager) cleanupLoop(interval time.Duration) {
	defer manager.cleanupWG.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-manager.ctx.Done():
			return
		case <-ticker.C:
			manager.cleanupExpired()
		}
	}
}

// cleanupExpired claims sessions using current activity while holding each
// session lock, removes them from the manager, then closes resources without a
// global manager lock held.
func (manager *Manager) cleanupExpired() {
	manager.mu.RLock()
	config := manager.config
	sessions := make([]*Session, 0, len(manager.sessions))
	for _, session := range manager.sessions {
		sessions = append(sessions, session)
	}
	manager.mu.RUnlock()

	now := manager.now()
	for _, session := range sessions {
		reason, age, idle, claimed := session.claimExpiration(now, config)
		if !claimed {
			continue
		}

		totalRecv, _, _ := session.GetStats()
		if reason == "maximum session lifetime exceeded" {
			log.Printf("Cleaning up session %s: exceeded max lifetime (%v)", session.id, age)
		} else {
			log.Printf("Cleaning up session %s: idle timeout (%v since last activity, received %d packets)",
				session.id, idle, totalRecv)
		}
		manager.finishClaimedClose(session, reason)
	}
}

// SetICEServers updates the ICE server snapshot used by future offers. Existing
// sessions keep the configuration they were created with.
func (manager *Manager) SetICEServers(servers []pion.ICEServer) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return
	}
	manager.config.ICEServers = cloneICEServers(servers)
}

func (manager *Manager) beginOffer(clientKey string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	if len(manager.sessions)+manager.pendingOffers >= manager.config.MaxSessions {
		return ErrSessionCapacity
	}
	if clientKey != "" {
		clientSessions := manager.pendingByClient[clientKey]
		for _, session := range manager.sessions {
			if session.clientKey == clientKey {
				clientSessions++
			}
		}
		if clientSessions >= manager.config.MaxSessionsPerClient {
			return ErrClientSessionCapacity
		}
		manager.pendingByClient[clientKey]++
	}
	manager.pendingOffers++
	// Add occurs under the same lock Shutdown uses to close admission. Once
	// Shutdown releases that lock, no later Add can race with offerWG.Wait.
	manager.offerWG.Add(1)
	return nil
}

func (manager *Manager) finishOffer(clientKey string) {
	manager.mu.Lock()
	manager.pendingOffers--
	if clientKey != "" {
		remaining := manager.pendingByClient[clientKey] - 1
		if remaining <= 0 {
			delete(manager.pendingByClient, clientKey)
		} else {
			manager.pendingByClient[clientKey] = remaining
		}
	}
	manager.mu.Unlock()
	manager.offerWG.Done()
}

func (manager *Manager) offerConfig() (Config, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return Config{}, ErrManagerClosed
	}
	config := manager.config
	config.ICEServers = cloneICEServers(manager.config.ICEServers)
	return config, nil
}

func (manager *Manager) negotiationError(ctx context.Context, session *Session) error {
	if manager.ctx.Err() != nil {
		return ErrManagerClosed
	}
	if err := ctx.Err(); err != nil {
		return ctx.Err()
	}
	if err := session.activeError(); err != nil {
		// Cancellation and peer failure can become visible between the checks
		// above and activeError. Recheck the owning contexts so callers get a
		// stable cause rather than a scheduler-dependent ErrSessionClosed.
		if manager.ctx.Err() != nil {
			return ErrManagerClosed
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return err
	}
	return nil
}

func (manager *Manager) ownsSessionWhileRunning(testID string, expected *Session) bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return !manager.closed && manager.sessions[testID] == expected
}

func (manager *Manager) registerSession(session *Session) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	if _, exists := manager.sessions[session.id]; exists {
		return fmt.Errorf("duplicate WebRTC session id %q", session.id)
	}
	if len(manager.sessions) >= manager.config.MaxSessions {
		return ErrSessionCapacity
	}
	if session.clientKey != "" {
		clientSessions := 0
		for _, existing := range manager.sessions {
			if existing.clientKey == session.clientKey {
				clientSessions++
			}
		}
		if clientSessions >= manager.config.MaxSessionsPerClient {
			return ErrClientSessionCapacity
		}
	}
	// Add under the same lock Shutdown uses to close registration. Once
	// Shutdown observes closed, no later sessionWG.Add can race its Wait.
	manager.sessionWG.Add(1)
	session.mu.Lock()
	session.onClosed = manager.sessionWG.Done
	session.mu.Unlock()
	manager.sessions[session.id] = session
	return nil
}

// HandleOffer processes an SDP offer without a per-client identity. Server
// Callers should use HandleOfferForClient so per-client ceilings apply.
func (manager *Manager) HandleOffer(ctx context.Context, offerSDP string, testProfile string) (answerSDP string, testID string, err error) {
	return manager.HandleOfferForClient(ctx, offerSDP, testProfile, "")
}

// HandleOfferForClient processes an SDP offer and attributes the resulting
// active session to clientKey for admission control. Negotiation is cancellable
// by the request or manager shutdown; an established session remains manager-owned.
func (manager *Manager) HandleOfferForClient(ctx context.Context, offerSDP string, testProfile string, clientKey string) (answerSDP string, testID string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := manager.beginOffer(clientKey); err != nil {
		return "", "", err
	}
	defer manager.finishOffer(clientKey)

	config, err := manager.offerConfig()
	if err != nil {
		return "", "", err
	}

	testID, err = manager.newID()
	if err != nil {
		return "", "", err
	}
	connection, err := manager.factory.New(pion.Configuration{
		ICEServers: cloneICEServers(config.ICEServers),
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create peer connection: %w", err)
	}

	session := newSession(manager.ctx, testID, testProfile, connection, manager.now)
	session.clientKey = clientKey
	if err := manager.registerSession(session); err != nil {
		session.closeDetached("session registration failed")
		return "", "", err
	}

	completed := false
	defer func() {
		if !completed {
			manager.closeSessionIf(session.id, session, "offer negotiation did not complete")
		}
	}()

	// Register callbacks only after the session is visible to every asynchronous
	// failure path, but before remote-description processing can start them.
	manager.setupConnectionCallbacks(session)
	if err := session.markNegotiating(); err != nil {
		if lifecycleErr := manager.negotiationError(ctx, session); lifecycleErr != nil {
			return "", "", lifecycleErr
		}
		return "", "", err
	}
	if err := manager.negotiationError(ctx, session); err != nil {
		return "", "", err
	}

	offer := pion.SessionDescription{
		Type: pion.SDPTypeOffer,
		SDP:  offerSDP,
	}
	if err := connection.SetRemoteDescription(offer); err != nil {
		return "", "", fmt.Errorf("failed to set remote description: %w", err)
	}
	if err := manager.negotiationError(ctx, session); err != nil {
		return "", "", err
	}

	answer, err := connection.CreateAnswer()
	if err != nil {
		return "", "", fmt.Errorf("failed to create answer: %w", err)
	}
	if err := manager.negotiationError(ctx, session); err != nil {
		return "", "", err
	}

	if err := connection.SetLocalDescription(answer); err != nil {
		return "", "", fmt.Errorf("failed to set local description: %w", err)
	}

	gatherTimer := time.NewTimer(config.ICEGatherTimeout)
	defer gatherTimer.Stop()
	select {
	case <-connection.GatheringComplete():
	case <-session.Done():
		if err := manager.negotiationError(ctx, session); err != nil {
			return "", "", err
		}
		return "", "", ErrSessionClosed
	case <-manager.ctx.Done():
		return "", "", manager.negotiationError(ctx, session)
	case <-ctx.Done():
		return "", "", manager.negotiationError(ctx, session)
	case <-gatherTimer.C:
		if err := manager.negotiationError(ctx, session); err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("ICE gathering timeout after %v", config.ICEGatherTimeout)
	}

	if err := manager.negotiationError(ctx, session); err != nil {
		return "", "", err
	}
	localDescription := connection.LocalDescription()
	if localDescription == nil || localDescription.SDP == "" {
		return "", "", errors.New("peer connection returned no local description")
	}
	if !manager.ownsSessionWhileRunning(testID, session) {
		if err := manager.negotiationError(ctx, session); err != nil {
			return "", "", err
		}
		return "", "", ErrSessionClosed
	}

	completed = true
	return localDescription.SDP, testID, nil
}

func (manager *Manager) setupConnectionCallbacks(session *Session) {
	connection := session.peerConnection
	connection.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		log.Printf("Session %s: connection state changed to %s", session.id, state.String())
		switch state {
		case pion.PeerConnectionStateConnecting:
			session.markConnecting(manager.now())
		case pion.PeerConnectionStateConnected:
			session.markConnected(manager.now())
		case pion.PeerConnectionStateDisconnected:
			session.beginDisconnectGrace(
				manager.now(),
				manager.disconnectGrace(),
				manager.afterFunc,
				func(generation uint64) {
					const reason = "peer connection disconnect grace expired"
					if session.claimDisconnectExpiration(generation, reason) {
						manager.finishClaimedClose(session, reason)
					}
				},
			)
		case pion.PeerConnectionStateFailed:
			manager.closeSessionIf(session.id, session, "peer connection failed")
		case pion.PeerConnectionStateClosed:
			manager.closeSessionIf(session.id, session, "peer connection closed")
		}
	})

	connection.OnICEConnectionStateChange(func(state pion.ICEConnectionState) {
		log.Printf("Session %s: ICE connection state changed to %s", session.id, state.String())
		switch state {
		case pion.ICEConnectionStateConnected, pion.ICEConnectionStateCompleted:
			session.markConnected(manager.now())
		case pion.ICEConnectionStateDisconnected:
			session.beginDisconnectGrace(
				manager.now(),
				manager.disconnectGrace(),
				manager.afterFunc,
				func(generation uint64) {
					const reason = "ICE disconnect grace expired"
					if session.claimDisconnectExpiration(generation, reason) {
						manager.finishClaimedClose(session, reason)
					}
				},
			)
		case pion.ICEConnectionStateFailed, pion.ICEConnectionStateClosed:
			manager.closeSessionIf(session.id, session, "ICE connection failed or closed")
		}
	})

	connection.OnDataChannel(func(channel dataChannel) {
		log.Printf("Session %s: data channel received: %s", session.id, channel.Label())
		if channel.Label() != "packet-loss" {
			if err := channel.Close(); err != nil {
				log.Printf("Session %s: failed to close unexpected data channel: %v", session.id, err)
			}
			return
		}
		if !session.attachDataChannel(channel) {
			if err := channel.Close(); err != nil {
				log.Printf("Session %s: failed to close rejected data channel: %v", session.id, err)
			}
			return
		}
		manager.setupPacketLossChannel(session, channel)
	})
}

func (manager *Manager) disconnectGrace() time.Duration {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.config.DisconnectGrace
}

// setupPacketLossChannel sets up handlers for the packet-loss data channel.
func (manager *Manager) setupPacketLossChannel(session *Session, channel dataChannel) {
	channel.OnOpen(func() {
		log.Printf("Session %s: packet-loss channel opened", session.id)
		session.markDataChannelOpen(manager.now())
	})

	channel.OnClose(func() {
		totalRecv, _, _ := session.GetStats()
		log.Printf("Session %s: packet-loss channel closed, received %d unique probes", session.id, totalRecv)
		session.detachDataChannel(channel)
	})

	channel.OnMessage(func(message pion.DataChannelMessage) {
		if !session.acceptsMessages() {
			return
		}

		frame, err := protocol.DecodePacketFrame(message.Data)
		if err != nil || frame.Acknowledgement {
			session.recordInvalidFrame()
			if err != nil {
				log.Printf("Session %s: invalid packet-loss frame: %v", session.id, err)
			}
			return
		}

		now := manager.now()
		session.markConnected(now)
		if session.recordProbe(frame.Sequence, now) {
			return
		}

		ackData := protocol.EncodeAckFrame(
			frame.Sequence,
			frame.SentAtUnixMilli,
			now.UnixMilli(),
		)
		if err := channel.Send(ackData); err != nil {
			session.recordAckFailure()
			log.Printf("Session %s: failed to send ack: %v", session.id, err)
			return
		}
		session.recordAckSent()
	})

	channel.OnError(func(err error) {
		log.Printf("Session %s: data channel error: %v", session.id, err)
	})
}

// PacketLossSnapshot returns an immutable snapshot of the server-observed
// packet-loss counters for an active session.
func (manager *Manager) PacketLossSnapshot(testID string) (PacketLossSnapshot, bool) {
	session, ok := manager.GetSession(testID)
	if !ok {
		return PacketLossSnapshot{}, false
	}
	return session.packetLossSnapshot(), true
}

// CompletePacketLossSession atomically verifies session ownership, removes the
// session from manager admission accounting, snapshots its packet counters, and
// closes its resources outside the manager lock. A false result deliberately
// does not distinguish a missing session from a session owned by another client.
func (manager *Manager) CompletePacketLossSession(testID, clientKey string) (PacketLossSnapshot, bool) {
	manager.mu.Lock()
	session, ok := manager.sessions[testID]
	if !ok || (session.clientKey != "" && session.clientKey != clientKey) {
		manager.mu.Unlock()
		return PacketLossSnapshot{}, false
	}
	delete(manager.sessions, testID)
	manager.mu.Unlock()

	snapshot := session.packetLossSnapshot()
	session.closeDetached("packet test report completed")
	return snapshot, true
}

// GetSession returns a session by ID.
func (manager *Manager) GetSession(testID string) (*Session, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	session, ok := manager.sessions[testID]
	return session, ok
}

// SessionCount returns the number of sessions currently owned by the manager.
func (manager *Manager) SessionCount() int {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return len(manager.sessions)
}

// CloseSession removes a session under the manager lock and closes its Pion
// resources only after the lock has been released.
func (manager *Manager) CloseSession(testID string) {
	manager.mu.Lock()
	session, ok := manager.sessions[testID]
	if ok {
		delete(manager.sessions, testID)
	}
	manager.mu.Unlock()

	if ok {
		session.closeDetached("session closed by report or caller")
	}
}

func (manager *Manager) closeSessionIf(testID string, session *Session, reason string) bool {
	manager.mu.Lock()
	current, ok := manager.sessions[testID]
	removed := ok && current == session
	if removed {
		delete(manager.sessions, testID)
	}
	manager.mu.Unlock()

	// Always close the target. It may already have been removed by another
	// owner, but closeDetached is concurrency-idempotent.
	session.closeDetached(reason)
	return removed
}

func (manager *Manager) finishClaimedClose(session *Session, reason string) {
	manager.mu.Lock()
	if current, ok := manager.sessions[session.id]; ok && current == session {
		delete(manager.sessions, session.id)
	}
	manager.mu.Unlock()
	session.closeDetached(reason)
}

// Shutdown stops the cleanup owner, removes all sessions, closes resources
// outside the manager lock, and waits until every close completes. It is safe to
// call concurrently and repeatedly.
func (manager *Manager) Shutdown() {
	manager.shutdownOnce.Do(func() {
		// Close admission first. beginOffer performs WaitGroup.Add under this
		// same lock, so waiting below cannot race with a later Add.
		manager.mu.Lock()
		manager.closed = true
		manager.mu.Unlock()
		manager.cancel()

		// Negotiators must finish installing or unwinding callbacks before
		// established resources are detached and closed.
		manager.offerWG.Wait()
		manager.cleanupWG.Wait()

		manager.mu.Lock()
		sessions := make([]*Session, 0, len(manager.sessions))
		for id, session := range manager.sessions {
			sessions = append(sessions, session)
			delete(manager.sessions, id)
		}
		manager.mu.Unlock()

		for _, session := range sessions {
			session.closeDetached("manager shutdown")
		}
		// This also waits for sessions removed by a failure, report, cleanup, or
		// disconnect callback immediately before shutdown took its map snapshot.
		manager.sessionWG.Wait()
	})
}

// Done closes after the session's data channel and peer connection have both
// been given their close calls.
func (session *Session) Done() <-chan struct{} {
	return session.done
}

// Snapshot returns a synchronized lifecycle snapshot.
func (session *Session) Snapshot() SessionSnapshot {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return SessionSnapshot{
		ID:           session.id,
		TestProfile:  session.testProfile,
		ClientKey:    session.clientKey,
		State:        session.state,
		CreatedAt:    session.createdAt,
		LastActivity: session.lastActivity,
		ClosedAt:     session.closedAt,
		CloseReason:  session.closeReason,
	}
}

func (session *Session) markNegotiating() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed {
		return session.closedErrorLocked()
	}
	if session.state == SessionStateNew {
		session.state = SessionStateNegotiating
	}
	return nil
}

func (session *Session) markConnecting(now time.Time) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed {
		return
	}
	// Do not cancel an established disconnect grace merely because the lower
	// layer reports another connecting transition. Only a real connected state
	// demonstrates recovery.
	if session.state != SessionStateDisconnected {
		session.state = SessionStateConnecting
	}
	session.lastActivity = now
}

func (session *Session) markConnected(now time.Time) {
	var timer stoppableTimer
	session.mu.Lock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed {
		session.mu.Unlock()
		return
	}
	session.state = SessionStateConnected
	session.lastActivity = now
	session.disconnectGeneration++
	timer = session.disconnectTimer
	session.disconnectTimer = nil
	session.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (session *Session) beginDisconnectGrace(
	now time.Time,
	grace time.Duration,
	afterFunc func(time.Duration, func()) stoppableTimer,
	onExpire func(uint64),
) {
	var oldTimer stoppableTimer
	session.mu.Lock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed || session.state == SessionStateDisconnected {
		session.mu.Unlock()
		return
	}
	session.state = SessionStateDisconnected
	session.lastActivity = now
	session.disconnectGeneration++
	generation := session.disconnectGeneration
	oldTimer = session.disconnectTimer
	session.disconnectTimer = nil
	session.mu.Unlock()

	if oldTimer != nil {
		oldTimer.Stop()
	}
	timer := afterFunc(grace, func() { onExpire(generation) })

	session.mu.Lock()
	if session.state == SessionStateDisconnected && session.disconnectGeneration == generation {
		session.disconnectTimer = timer
		session.mu.Unlock()
		return
	}
	session.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (session *Session) claimDisconnectExpiration(generation uint64, reason string) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SessionStateDisconnected || session.disconnectGeneration != generation {
		return false
	}
	session.state = SessionStateClosing
	session.disconnectGeneration++
	session.disconnectTimer = nil
	if session.closeReason == "" {
		session.closeReason = reason
	}
	return true
}

func (session *Session) claimExpiration(now time.Time, config Config) (reason string, age, idle time.Duration, claimed bool) {
	var timer stoppableTimer
	session.mu.Lock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed {
		session.mu.Unlock()
		return "", 0, 0, false
	}
	age = now.Sub(session.createdAt)
	idle = now.Sub(session.lastActivity)
	if age < 0 {
		age = 0
	}
	if idle < 0 {
		idle = 0
	}

	switch {
	case age > config.MaxSessionTime:
		reason = "maximum session lifetime exceeded"
	case idle > config.IdleTimeout:
		reason = "idle session timeout exceeded"
	default:
		session.mu.Unlock()
		return "", age, idle, false
	}

	session.state = SessionStateClosing
	session.disconnectGeneration++
	timer = session.disconnectTimer
	session.disconnectTimer = nil
	if session.closeReason == "" {
		session.closeReason = reason
	}
	session.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	return reason, age, idle, true
}

func (session *Session) closeDetached(reason string) {
	owner := false
	var channel dataChannel
	var timer stoppableTimer
	var onClosed func()

	session.closeOnce.Do(func() {
		owner = true
		session.mu.Lock()
		session.state = SessionStateClosing
		if session.closeReason == "" {
			session.closeReason = reason
		}
		session.disconnectGeneration++
		timer = session.disconnectTimer
		session.disconnectTimer = nil
		channel = session.dataChannel
		session.dataChannel = nil
		onClosed = session.onClosed
		session.mu.Unlock()
	})
	if !owner {
		return
	}

	session.cancel()
	if timer != nil {
		timer.Stop()
	}
	if channel != nil {
		if err := channel.Close(); err != nil {
			log.Printf("Session %s: failed to close data channel: %v", session.id, err)
		}
	}
	if session.peerConnection != nil {
		if err := session.peerConnection.Close(); err != nil {
			log.Printf("Session %s: failed to close peer connection: %v", session.id, err)
		}
	}

	session.mu.Lock()
	session.state = SessionStateClosed
	session.closedAt = session.now()
	session.mu.Unlock()
	close(session.done)
	if onClosed != nil {
		onClosed()
	}
}

func (session *Session) activeError() error {
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed || session.ctx.Err() != nil {
		return session.closedErrorLocked()
	}
	return nil
}

func (session *Session) closedError() error {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.closedErrorLocked()
}

func (session *Session) closedErrorLocked() error {
	if session.closeReason == "" {
		return ErrSessionClosed
	}
	return fmt.Errorf("%w: %s", ErrSessionClosed, session.closeReason)
}

func (session *Session) acceptsMessages() bool {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.state != SessionStateClosing && session.state != SessionStateClosed && session.ctx.Err() == nil
}

func (session *Session) attachDataChannel(channel dataChannel) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed || session.ctx.Err() != nil {
		return false
	}
	if session.dataChannelClaimed || session.dataChannel != nil {
		return false
	}
	session.dataChannelClaimed = true
	session.dataChannel = channel
	return true
}

func (session *Session) detachDataChannel(channel dataChannel) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.dataChannel == channel {
		session.dataChannel = nil
	}
}

func (session *Session) markDataChannelOpen(now time.Time) bool {
	var timer stoppableTimer
	session.mu.Lock()
	if session.state == SessionStateClosing || session.state == SessionStateClosed || session.ctx.Err() != nil {
		session.mu.Unlock()
		return false
	}
	session.state = SessionStateConnected
	session.lastActivity = now
	session.disconnectGeneration++
	timer = session.disconnectTimer
	session.disconnectTimer = nil
	session.stats.mu.Lock()
	session.stats.StartTime = now
	session.stats.mu.Unlock()
	session.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	return true
}

func (session *Session) recordInvalidFrame() {
	session.stats.mu.Lock()
	session.stats.InvalidFrames++
	session.stats.mu.Unlock()
}

// recordProbe returns true for a duplicate frame.
func (session *Session) recordProbe(sequence uint32, now time.Time) bool {
	session.stats.mu.Lock()
	defer session.stats.mu.Unlock()
	if _, exists := session.stats.ReceivedSequences[sequence]; exists {
		session.stats.DuplicateFrames++
		session.stats.LastRecvTime = now
		return true
	}
	session.stats.ReceivedSequences[sequence] = struct{}{}
	session.stats.TotalRecv++
	session.stats.LastSeq = int(sequence)
	session.stats.LastRecvTime = now
	return false
}

func (session *Session) recordAckFailure() {
	session.stats.mu.Lock()
	session.stats.AckSendFailures++
	session.stats.mu.Unlock()
}

func (session *Session) recordAckSent() {
	session.stats.mu.Lock()
	session.stats.AcknowledgementsSent++
	session.stats.mu.Unlock()
}

func (session *Session) packetLossSnapshot() PacketLossSnapshot {
	session.stats.mu.RLock()
	defer session.stats.mu.RUnlock()
	return PacketLossSnapshot{
		FrameSizeBytes:       protocol.PacketFrameSize,
		ForwardReceived:      len(session.stats.ReceivedSequences),
		AcknowledgementsSent: session.stats.AcknowledgementsSent,
		DuplicateFrames:      session.stats.DuplicateFrames,
		InvalidFrames:        session.stats.InvalidFrames,
		AckSendFailures:      session.stats.AckSendFailures,
	}
}

// GetStats returns the current session statistics.
func (session *Session) GetStats() (totalRecv int, lastSeq int, duration time.Duration) {
	session.stats.mu.RLock()
	totalRecv = session.stats.TotalRecv
	lastSeq = session.stats.LastSeq
	startTime := session.stats.StartTime
	session.stats.mu.RUnlock()

	duration = session.now().Sub(startTime)
	if duration < 0 {
		duration = 0
	}
	return totalRecv, lastSeq, duration
}
