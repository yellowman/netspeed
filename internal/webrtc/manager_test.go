package webrtc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v3"

	"github.com/yellowman/netspeed/internal/protocol"
)

type manualTimer struct {
	mu      sync.Mutex
	stopped bool
	fn      func()
}

func (timer *manualTimer) Stop() bool {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	active := !timer.stopped
	timer.stopped = true
	return active
}

func (timer *manualTimer) fire(force bool) {
	timer.mu.Lock()
	stopped := timer.stopped
	callback := timer.fn
	timer.mu.Unlock()
	if force || !stopped {
		callback()
	}
}

type manualScheduler struct {
	mu     sync.Mutex
	timers []*manualTimer
}

func (scheduler *manualScheduler) afterFunc(_ time.Duration, callback func()) stoppableTimer {
	timer := &manualTimer{fn: callback}
	scheduler.mu.Lock()
	scheduler.timers = append(scheduler.timers, timer)
	scheduler.mu.Unlock()
	return timer
}

func (scheduler *manualScheduler) latest(t *testing.T) *manualTimer {
	t.Helper()
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if len(scheduler.timers) == 0 {
		t.Fatal("no timer was scheduled")
	}
	return scheduler.timers[len(scheduler.timers)-1]
}

type fakePeerConnectionFactory struct {
	mu      sync.Mutex
	configs []pion.Configuration
	new     func(pion.Configuration) (peerConnection, error)
}

func (factory *fakePeerConnectionFactory) New(config pion.Configuration) (peerConnection, error) {
	factory.mu.Lock()
	factory.configs = append(factory.configs, config)
	newConnection := factory.new
	factory.mu.Unlock()
	if newConnection == nil {
		return newFakePeerConnection(true), nil
	}
	return newConnection(config)
}

func (factory *fakePeerConnectionFactory) latestConfig(t *testing.T) pion.Configuration {
	t.Helper()
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.configs) == 0 {
		t.Fatal("peer connection factory was not called")
	}
	return factory.configs[len(factory.configs)-1]
}

type fakePeerConnection struct {
	mu sync.Mutex

	local  *pion.SessionDescription
	gather chan struct{}

	onConnection func(pion.PeerConnectionState)
	onICE        func(pion.ICEConnectionState)
	onData       func(dataChannel)

	failWhenConnectionHandlerInstalled bool
	closeEmitsClosedState              bool
	onClose                            func()
	localSet                           chan struct{}
	localSetOnce                       sync.Once
	closeCount                         atomic.Int32
}

func newFakePeerConnection(gathered bool) *fakePeerConnection {
	gather := make(chan struct{})
	if gathered {
		close(gather)
	}
	return &fakePeerConnection{gather: gather, localSet: make(chan struct{})}
}

func (connection *fakePeerConnection) OnConnectionStateChange(handler func(pion.PeerConnectionState)) {
	connection.mu.Lock()
	connection.onConnection = handler
	failImmediately := connection.failWhenConnectionHandlerInstalled
	connection.mu.Unlock()
	if failImmediately {
		handler(pion.PeerConnectionStateFailed)
	}
}

func (connection *fakePeerConnection) OnICEConnectionStateChange(handler func(pion.ICEConnectionState)) {
	connection.mu.Lock()
	connection.onICE = handler
	connection.mu.Unlock()
}

func (connection *fakePeerConnection) OnDataChannel(handler func(dataChannel)) {
	connection.mu.Lock()
	connection.onData = handler
	connection.mu.Unlock()
}

func (connection *fakePeerConnection) SetRemoteDescription(pion.SessionDescription) error { return nil }

func (connection *fakePeerConnection) CreateAnswer() (pion.SessionDescription, error) {
	return pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: "fake-answer"}, nil
}

func (connection *fakePeerConnection) SetLocalDescription(description pion.SessionDescription) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	copy := description
	connection.local = &copy
	connection.localSetOnce.Do(func() { close(connection.localSet) })
	return nil
}

func (connection *fakePeerConnection) LocalDescription() *pion.SessionDescription {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.local == nil {
		return nil
	}
	copy := *connection.local
	return &copy
}

func (connection *fakePeerConnection) GatheringComplete() <-chan struct{} { return connection.gather }

func (connection *fakePeerConnection) Close() error {
	connection.closeCount.Add(1)
	connection.mu.Lock()
	callback := connection.onConnection
	emitClosed := connection.closeEmitsClosedState
	onClose := connection.onClose
	connection.mu.Unlock()
	if onClose != nil {
		onClose()
	}
	if emitClosed && callback != nil {
		callback(pion.PeerConnectionStateClosed)
	}
	return nil
}

func (connection *fakePeerConnection) emitConnection(state pion.PeerConnectionState) {
	connection.mu.Lock()
	callback := connection.onConnection
	connection.mu.Unlock()
	if callback != nil {
		callback(state)
	}
}

func (connection *fakePeerConnection) emitICE(state pion.ICEConnectionState) {
	connection.mu.Lock()
	callback := connection.onICE
	connection.mu.Unlock()
	if callback != nil {
		callback(state)
	}
}

func (connection *fakePeerConnection) emitDataChannel(channel dataChannel) {
	connection.mu.Lock()
	callback := connection.onData
	connection.mu.Unlock()
	if callback != nil {
		callback(channel)
	}
}

type fakeDataChannel struct {
	label string

	mu         sync.Mutex
	onOpen     func()
	onClose    func()
	onMessage  func(pion.DataChannelMessage)
	onError    func(error)
	sent       [][]byte
	sendErr    error
	closeCount atomic.Int32
}

func (channel *fakeDataChannel) Label() string { return channel.label }

func (channel *fakeDataChannel) OnOpen(handler func()) {
	channel.mu.Lock()
	channel.onOpen = handler
	channel.mu.Unlock()
}

func (channel *fakeDataChannel) OnClose(handler func()) {
	channel.mu.Lock()
	channel.onClose = handler
	channel.mu.Unlock()
}

func (channel *fakeDataChannel) OnMessage(handler func(pion.DataChannelMessage)) {
	channel.mu.Lock()
	channel.onMessage = handler
	channel.mu.Unlock()
}

func (channel *fakeDataChannel) OnError(handler func(error)) {
	channel.mu.Lock()
	channel.onError = handler
	channel.mu.Unlock()
}

func (channel *fakeDataChannel) Send(payload []byte) error {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if channel.sendErr != nil {
		return channel.sendErr
	}
	channel.sent = append(channel.sent, append([]byte(nil), payload...))
	return nil
}

func (channel *fakeDataChannel) Close() error {
	channel.closeCount.Add(1)
	channel.mu.Lock()
	callback := channel.onClose
	channel.mu.Unlock()
	if callback != nil {
		callback()
	}
	return nil
}

func (channel *fakeDataChannel) emitOpen() {
	channel.mu.Lock()
	callback := channel.onOpen
	channel.mu.Unlock()
	if callback != nil {
		callback()
	}
}

func (channel *fakeDataChannel) emitMessage(payload []byte) {
	channel.mu.Lock()
	callback := channel.onMessage
	channel.mu.Unlock()
	if callback != nil {
		callback(pion.DataChannelMessage{Data: payload})
	}
}

func fixedID(value string) func() (string, error) {
	return func() (string, error) { return value, nil }
}

func newTestManager(t *testing.T, factory peerConnectionFactory, scheduler *manualScheduler) *Manager {
	t.Helper()
	if factory == nil {
		factory = &fakePeerConnectionFactory{}
	}
	if scheduler == nil {
		scheduler = &manualScheduler{}
	}
	manager := newManager(
		Config{
			IdleTimeout:      time.Minute,
			MaxSessionTime:   time.Hour,
			CleanupTicker:    time.Hour,
			DisconnectGrace:  time.Second,
			ICEGatherTimeout: time.Second,
		},
		managerDependencies{
			factory:   factory,
			now:       time.Now,
			afterFunc: scheduler.afterFunc,
			newID:     fixedID("11111111-2222-4333-8444-555555555555"),
		},
	)
	t.Cleanup(manager.Shutdown)
	return manager
}

func TestSessionCloseIsConcurrentAndIdempotent(t *testing.T) {
	peer := newFakePeerConnection(true)
	peer.closeEmitsClosedState = true
	channel := &fakeDataChannel{label: "packet-loss"}
	session := newSession(context.Background(), "session", "normal", peer, time.Now)
	if !session.attachDataChannel(channel) {
		t.Fatal("failed to attach data channel")
	}

	const callers = 128
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			session.closeDetached("concurrent close")
		}()
	}
	wait.Wait()

	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
	if got := channel.closeCount.Load(); got != 1 {
		t.Fatalf("data channel close count = %d, want 1", got)
	}
	if snapshot := session.Snapshot(); snapshot.State != SessionStateClosed {
		t.Fatalf("state = %s, want closed", snapshot.State)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("session Done was not closed")
	}
}

func TestCloseSessionDetachesBeforeResourceClose(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	peer := newFakePeerConnection(true)
	var sawDetached atomic.Bool
	peer.onClose = func() {
		_, present := manager.GetSession("session")
		sawDetached.Store(!present)
		// This read would deadlock if the manager write lock were held around
		// peer teardown.
		_ = manager.SessionCount()
	}
	peer.closeEmitsClosedState = true
	session := newSession(manager.ctx, "session", "normal", peer, time.Now)
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}

	done := make(chan struct{})
	go func() {
		manager.CloseSession("session")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("CloseSession deadlocked during callback re-entry")
	}
	if !sawDetached.Load() {
		t.Fatal("peer was closed before the session was detached")
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestStaleCallbackCannotRemoveReusedSessionID(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	oldPeer := newFakePeerConnection(true)
	oldSession := newSession(manager.ctx, "session", "normal", oldPeer, time.Now)
	if err := manager.registerSession(oldSession); err != nil {
		t.Fatalf("register old session: %v", err)
	}
	manager.setupConnectionCallbacks(oldSession)
	manager.CloseSession("session")

	newPeer := newFakePeerConnection(true)
	newSession := newSession(manager.ctx, "session", "normal", newPeer, time.Now)
	if err := manager.registerSession(newSession); err != nil {
		t.Fatalf("register replacement session: %v", err)
	}
	manager.setupConnectionCallbacks(newSession)

	// Model a delayed callback from the already-closed old Pion connection.
	// The ID now belongs to another pointer, so the callback must not remove it.
	oldPeer.emitConnection(pion.PeerConnectionStateFailed)
	current, ok := manager.GetSession("session")
	if !ok || current != newSession {
		t.Fatal("stale callback removed the replacement session")
	}
	if got := newPeer.closeCount.Load(); got != 0 {
		t.Fatalf("replacement peer close count = %d, want 0", got)
	}

	manager.CloseSession("session")
}

func TestDisconnectGraceRecoversThenExpires(t *testing.T) {
	scheduler := &manualScheduler{}
	manager := newTestManager(t, nil, scheduler)
	peer := newFakePeerConnection(true)
	peer.closeEmitsClosedState = true
	session := newSession(manager.ctx, "session", "normal", peer, time.Now)
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}
	manager.setupConnectionCallbacks(session)

	peer.emitConnection(pion.PeerConnectionStateDisconnected)
	firstTimer := scheduler.latest(t)
	if state := session.Snapshot().State; state != SessionStateDisconnected {
		t.Fatalf("state = %s, want disconnected", state)
	}

	peer.emitConnection(pion.PeerConnectionStateConnected)
	// Model the timer Stop race where the callback was already queued. The
	// generation check must still reject the stale expiration.
	firstTimer.fire(true)
	if got := manager.SessionCount(); got != 1 {
		t.Fatalf("session count after recovery = %d, want 1", got)
	}
	if got := peer.closeCount.Load(); got != 0 {
		t.Fatalf("peer closed after recovery: %d", got)
	}
	if state := session.Snapshot().State; state != SessionStateConnected {
		t.Fatalf("state after recovery = %s, want connected", state)
	}

	peer.emitICE(pion.ICEConnectionStateDisconnected)
	scheduler.latest(t).fire(false)
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count after grace expiry = %d, want 0", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count after grace expiry = %d, want 1", got)
	}
	snapshot := session.Snapshot()
	if snapshot.State != SessionStateClosed || snapshot.CloseReason != "ICE disconnect grace expired" {
		t.Fatalf("snapshot after expiry = %+v", snapshot)
	}
}

func TestCleanupExpirationClosesOutsideManagerLock(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	peer := newFakePeerConnection(true)
	peer.onClose = func() { _ = manager.SessionCount() }
	session := newSession(manager.ctx, "expired", "normal", peer, time.Now)
	session.createdAt = time.Now().Add(-2 * time.Hour)
	session.lastActivity = session.createdAt
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}

	done := make(chan struct{})
	go func() {
		manager.cleanupExpired()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup deadlocked during callback re-entry")
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count = %d, want 0", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestHandleOfferRegistersBeforeImmediateFailureCallback(t *testing.T) {
	peer := newFakePeerConnection(true)
	peer.failWhenConnectionHandlerInstalled = true
	peer.closeEmitsClosedState = true
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		return peer, nil
	}}
	manager := newTestManager(t, factory, nil)

	_, _, err := manager.HandleOffer(context.Background(), "offer", "normal")
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("HandleOffer error = %v, want ErrSessionClosed", err)
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session leaked after immediate callback: %d", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestHandleOfferSuccessPreservesProfile(t *testing.T) {
	peer := newFakePeerConnection(true)
	peer.closeEmitsClosedState = true
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		return peer, nil
	}}
	manager := newTestManager(t, factory, nil)

	answer, id, err := manager.HandleOffer(context.Background(), "offer", "quick")
	if err != nil {
		t.Fatalf("HandleOffer: %v", err)
	}
	if answer != "fake-answer" {
		t.Fatalf("answer = %q, want fake-answer", answer)
	}
	session, ok := manager.GetSession(id)
	if !ok {
		t.Fatal("successful session was not retained")
	}
	if got := session.Snapshot().TestProfile; got != "quick" {
		t.Fatalf("profile = %q, want quick", got)
	}

	manager.CloseSession(id)
	manager.CloseSession(id)
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestHandleOfferRejectsAlreadyCanceledContextBeforeCreatingPeer(t *testing.T) {
	peer := newFakePeerConnection(false)
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		t.Fatal("peer factory called for an already-canceled request")
		return peer, nil
	}}
	manager := newTestManager(t, factory, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := manager.HandleOffer(ctx, "offer", "normal")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleOffer error = %v, want context.Canceled", err)
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count after rejected request = %d, want 0", got)
	}
	if got := peer.closeCount.Load(); got != 0 {
		t.Fatalf("peer close count = %d, want 0 because no peer was created", got)
	}
}

func TestHandleOfferCancellationDuringGatherRemovesSession(t *testing.T) {
	peer := newFakePeerConnection(false)
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		return peer, nil
	}}
	manager := newTestManager(t, factory, nil)
	ctx, cancel := context.WithCancel(context.Background())

	result := make(chan error, 1)
	go func() {
		_, _, err := manager.HandleOffer(ctx, "offer", "normal")
		result <- err
	}()
	select {
	case <-peer.localSet:
	case <-time.After(time.Second):
		t.Fatal("offer did not reach ICE gathering")
	}
	if got := manager.SessionCount(); got != 1 {
		t.Fatalf("session count during gathering = %d, want 1", got)
	}

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HandleOffer error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock ICE gathering")
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session leaked after cancellation: %d", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestHandleOfferGatherTimeoutRemovesSession(t *testing.T) {
	peer := newFakePeerConnection(false)
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		return peer, nil
	}}
	manager := newManager(
		Config{
			IdleTimeout:      time.Minute,
			MaxSessionTime:   time.Hour,
			CleanupTicker:    time.Hour,
			DisconnectGrace:  time.Second,
			ICEGatherTimeout: 5 * time.Millisecond,
		},
		managerDependencies{
			factory:   factory,
			now:       time.Now,
			afterFunc: (&manualScheduler{}).afterFunc,
			newID:     fixedID("11111111-2222-4333-8444-555555555555"),
		},
	)
	t.Cleanup(manager.Shutdown)

	_, _, err := manager.HandleOffer(context.Background(), "offer", "normal")
	if err == nil || !strings.Contains(err.Error(), "ICE gathering timeout") {
		t.Fatalf("HandleOffer error = %v, want ICE gathering timeout", err)
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session leaked after gathering timeout: %d", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestShutdownCancelsNegotiationAndWaitsForUnwind(t *testing.T) {
	peer := newFakePeerConnection(false)
	factoryCalled := make(chan struct{})
	var factoryOnce sync.Once
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		factoryOnce.Do(func() { close(factoryCalled) })
		return peer, nil
	}}
	manager := newTestManager(t, factory, nil)

	offerDone := make(chan error, 1)
	go func() {
		_, _, err := manager.HandleOffer(context.Background(), "offer", "normal")
		offerDone <- err
	}()
	select {
	case <-factoryCalled:
	case <-time.After(time.Second):
		t.Fatal("offer did not begin")
	}

	shutdownDone := make(chan struct{})
	go func() {
		manager.Shutdown()
		close(shutdownDone)
	}()

	select {
	case err := <-offerDone:
		if !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("offer error = %v, want ErrManagerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("offer did not unwind after shutdown")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not complete")
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count after shutdown = %d, want 0", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
}

func TestShutdownIsConcurrentAndIdempotent(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	const sessions = 12
	peers := make([]*fakePeerConnection, 0, sessions)
	for index := 0; index < sessions; index++ {
		peer := newFakePeerConnection(true)
		peer.closeEmitsClosedState = true
		peers = append(peers, peer)
		id := string(rune('a' + index))
		session := newSession(manager.ctx, id, "normal", peer, time.Now)
		if err := manager.registerSession(session); err != nil {
			t.Fatalf("register session %d: %v", index, err)
		}
	}

	const callers = 32
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			manager.Shutdown()
		}()
	}
	wait.Wait()

	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count after shutdown = %d, want 0", got)
	}
	for index, peer := range peers {
		if got := peer.closeCount.Load(); got != 1 {
			t.Fatalf("peer %d close count = %d, want 1", index, got)
		}
	}
	if _, _, err := manager.HandleOffer(context.Background(), "offer", "normal"); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("post-shutdown HandleOffer error = %v, want ErrManagerClosed", err)
	}
}

func TestShutdownWaitsForSessionRemovedDuringInFlightClose(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	peer := newFakePeerConnection(true)
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	var closeStartedOnce sync.Once
	peer.onClose = func() {
		closeStartedOnce.Do(func() { close(closeStarted) })
		<-releaseClose
	}
	session := newSession(manager.ctx, "session", "normal", peer, time.Now)
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}

	closeDone := make(chan struct{})
	go func() {
		manager.CloseSession("session")
		close(closeDone)
	}()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("session close did not reach the blocking peer close")
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count during detached close = %d, want 0", got)
	}

	shutdownDone := make(chan struct{})
	go func() {
		manager.Shutdown()
		close(shutdownDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		manager.mu.RLock()
		closed := manager.closed
		manager.mu.RUnlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not close admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before the detached session finished closing")
	default:
	}

	close(releaseClose)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("CloseSession did not finish after releasing peer close")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish after the detached session closed")
	}
}

func TestICEServerConfigurationIsDeepCopied(t *testing.T) {
	peer := newFakePeerConnection(true)
	factory := &fakePeerConnectionFactory{new: func(pion.Configuration) (peerConnection, error) {
		return peer, nil
	}}
	manager := newTestManager(t, factory, nil)

	urls := []string{"stun:original.example"}
	servers := []pion.ICEServer{{URLs: urls}}
	manager.SetICEServers(servers)
	urls[0] = "stun:mutated.example"
	servers[0].URLs[0] = "stun:also-mutated.example"

	_, id, err := manager.HandleOffer(context.Background(), "offer", "normal")
	if err != nil {
		t.Fatalf("HandleOffer: %v", err)
	}
	manager.CloseSession(id)
	captured := factory.latestConfig(t)
	if got := captured.ICEServers[0].URLs[0]; got != "stun:original.example" {
		t.Fatalf("captured ICE URL = %q, want original", got)
	}
}

func TestDataChannelOwnershipRejectsUnexpectedAndDuplicateChannels(t *testing.T) {
	scheduler := &manualScheduler{}
	manager := newTestManager(t, nil, scheduler)
	peer := newFakePeerConnection(true)
	session := newSession(manager.ctx, "session", "normal", peer, time.Now)
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}
	manager.setupConnectionCallbacks(session)

	unexpected := &fakeDataChannel{label: "other"}
	peer.emitDataChannel(unexpected)
	if got := unexpected.closeCount.Load(); got != 1 {
		t.Fatalf("unexpected channel close count = %d, want 1", got)
	}

	primary := &fakeDataChannel{label: "packet-loss"}
	peer.emitDataChannel(primary)
	primary.emitOpen()
	peer.emitConnection(pion.PeerConnectionStateDisconnected)
	disconnectTimer := scheduler.latest(t)
	primary.emitMessage(protocol.EncodeProbeFrame(7, time.Now().UnixMilli()))
	// A valid frame is direct evidence that the data path recovered. Even a
	// timer callback already queued before Stop must not close the session.
	disconnectTimer.fire(true)
	if got := manager.SessionCount(); got != 1 {
		t.Fatalf("valid data did not cancel disconnect grace; sessions=%d", got)
	}
	duplicate := &fakeDataChannel{label: "packet-loss"}
	peer.emitDataChannel(duplicate)
	if got := duplicate.closeCount.Load(); got != 1 {
		t.Fatalf("duplicate channel close count = %d, want 1", got)
	}

	// A remote close releases the concrete channel reference but not the
	// session's one-channel claim. A later replacement channel is still late
	// and must not become an unowned second test path.
	primary.Close()
	late := &fakeDataChannel{label: "packet-loss"}
	peer.emitDataChannel(late)
	if got := late.closeCount.Load(); got != 1 {
		t.Fatalf("late channel close count = %d, want 1", got)
	}

	snapshot, ok := manager.PacketLossSnapshot("session")
	if !ok || snapshot.ForwardReceived != 1 || snapshot.AcknowledgementsSent != 1 {
		t.Fatalf("packet snapshot = %+v, ok=%v", snapshot, ok)
	}
	manager.CloseSession("session")
	if got := primary.closeCount.Load(); got != 1 {
		t.Fatalf("primary channel close count = %d, want 1", got)
	}
}

func TestConcurrentLifecycleStatsConfigurationAndClose(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	peer := newFakePeerConnection(true)
	session := newSession(manager.ctx, "session", "normal", peer, time.Now)
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}

	const iterations = 500
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < iterations; index++ {
				now := time.Now()
				switch worker % 4 {
				case 0:
					session.markConnecting(now)
					session.markConnected(now)
				case 1:
					session.recordProbe(uint32(index%32), now)
					session.recordAckSent()
				case 2:
					_ = session.Snapshot()
					_ = session.packetLossSnapshot()
				case 3:
					manager.SetICEServers([]pion.ICEServer{{URLs: []string{"stun:example"}}})
					_, _ = manager.offerConfig()
				}
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		manager.CloseSession("session")
	}()
	wait.Wait()

	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count = %d, want 1", got)
	}
	if state := session.Snapshot().State; state != SessionStateClosed {
		t.Fatalf("final state = %s, want closed", state)
	}
}

func TestRandomSessionIDHasUUIDv4Shape(t *testing.T) {
	id, err := randomSessionID()
	if err != nil {
		t.Fatalf("randomSessionID: %v", err)
	}
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("unexpected session ID shape: %q", id)
	}
	if id[14] != '4' {
		t.Fatalf("version nibble = %q, want 4", id[14])
	}
	if id[19] != '8' && id[19] != '9' && id[19] != 'a' && id[19] != 'b' {
		t.Fatalf("variant nibble = %q, want 8, 9, a, or b", id[19])
	}
}

func TestOfferReservationsEnforceGlobalAndPerClientCapacityBeforePeerCreation(t *testing.T) {
	manager := newManager(
		Config{
			IdleTimeout:          time.Minute,
			MaxSessionTime:       time.Hour,
			CleanupTicker:        time.Hour,
			DisconnectGrace:      time.Second,
			ICEGatherTimeout:     time.Second,
			MaxSessions:          2,
			MaxSessionsPerClient: 1,
		},
		managerDependencies{
			factory:   &fakePeerConnectionFactory{},
			now:       time.Now,
			afterFunc: (&manualScheduler{}).afterFunc,
			newID:     fixedID("11111111-2222-4333-8444-555555555555"),
		},
	)
	t.Cleanup(manager.Shutdown)

	if err := manager.beginOffer("client-a"); err != nil {
		t.Fatalf("reserve client-a: %v", err)
	}
	defer manager.finishOffer("client-a")
	if err := manager.beginOffer("client-a"); !errors.Is(err, ErrClientSessionCapacity) {
		t.Fatalf("second client-a reservation error=%v; want per-client capacity", err)
	}
	if err := manager.beginOffer("client-b"); err != nil {
		t.Fatalf("reserve client-b: %v", err)
	}
	defer manager.finishOffer("client-b")
	if err := manager.beginOffer("client-c"); !errors.Is(err, ErrSessionCapacity) {
		t.Fatalf("third reservation error=%v; want global capacity", err)
	}
}

func TestCompletePacketLossSessionRequiresCreatingClient(t *testing.T) {
	manager := newTestManager(t, nil, nil)
	peer := newFakePeerConnection(true)
	session := newSession(manager.ctx, "owned-session", "normal", peer, time.Now)
	session.clientKey = "198.51.100.10"
	session.recordProbe(7, time.Now())
	if err := manager.registerSession(session); err != nil {
		t.Fatalf("register session: %v", err)
	}

	if snapshot, ok := manager.CompletePacketLossSession("owned-session", "198.51.100.11"); ok {
		t.Fatalf("other client completed session: %+v", snapshot)
	}
	if got := manager.SessionCount(); got != 1 {
		t.Fatalf("session count after ownership rejection=%d; want 1", got)
	}

	snapshot, ok := manager.CompletePacketLossSession("owned-session", "198.51.100.10")
	if !ok || snapshot.ForwardReceived != 1 {
		t.Fatalf("owner completion snapshot=%+v ok=%v", snapshot, ok)
	}
	if got := manager.SessionCount(); got != 0 {
		t.Fatalf("session count after completion=%d; want 0", got)
	}
	if got := peer.closeCount.Load(); got != 1 {
		t.Fatalf("peer close count=%d; want 1", got)
	}
}
