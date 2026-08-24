package webrtc

import pion "github.com/pion/webrtc/v3"

// peerConnection is the lifecycle surface Manager needs from Pion. Keeping the
// surface small lets the session owner be tested deterministically without a
// live ICE stack.
type peerConnection interface {
	OnConnectionStateChange(func(pion.PeerConnectionState))
	OnICEConnectionStateChange(func(pion.ICEConnectionState))
	OnDataChannel(func(dataChannel))
	SetRemoteDescription(pion.SessionDescription) error
	CreateAnswer() (pion.SessionDescription, error)
	SetLocalDescription(pion.SessionDescription) error
	LocalDescription() *pion.SessionDescription
	GatheringComplete() <-chan struct{}
	Close() error
}

type dataChannel interface {
	Label() string
	OnOpen(func())
	OnClose(func())
	OnMessage(func(pion.DataChannelMessage))
	OnError(func(error))
	Send([]byte) error
	Close() error
}

type peerConnectionFactory interface {
	New(pion.Configuration) (peerConnection, error)
}

var (
	_ peerConnectionFactory = pionPeerConnectionFactory{}
	_ peerConnection        = (*pionPeerConnection)(nil)
	_ dataChannel           = (*pionDataChannel)(nil)
)

type pionPeerConnectionFactory struct{}

func (pionPeerConnectionFactory) New(config pion.Configuration) (peerConnection, error) {
	pc, err := pion.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}
	return &pionPeerConnection{pc: pc}, nil
}

type pionPeerConnection struct {
	pc *pion.PeerConnection
}

func (p *pionPeerConnection) OnConnectionStateChange(handler func(pion.PeerConnectionState)) {
	p.pc.OnConnectionStateChange(handler)
}

func (p *pionPeerConnection) OnICEConnectionStateChange(handler func(pion.ICEConnectionState)) {
	p.pc.OnICEConnectionStateChange(handler)
}

func (p *pionPeerConnection) OnDataChannel(handler func(dataChannel)) {
	p.pc.OnDataChannel(func(dc *pion.DataChannel) {
		handler(&pionDataChannel{dc: dc})
	})
}

func (p *pionPeerConnection) SetRemoteDescription(description pion.SessionDescription) error {
	return p.pc.SetRemoteDescription(description)
}

func (p *pionPeerConnection) CreateAnswer() (pion.SessionDescription, error) {
	return p.pc.CreateAnswer(nil)
}

func (p *pionPeerConnection) SetLocalDescription(description pion.SessionDescription) error {
	return p.pc.SetLocalDescription(description)
}

func (p *pionPeerConnection) LocalDescription() *pion.SessionDescription {
	return p.pc.LocalDescription()
}

func (p *pionPeerConnection) GatheringComplete() <-chan struct{} {
	return pion.GatheringCompletePromise(p.pc)
}

func (p *pionPeerConnection) Close() error {
	return p.pc.Close()
}

type pionDataChannel struct {
	dc *pion.DataChannel
}

func (d *pionDataChannel) Label() string {
	return d.dc.Label()
}

func (d *pionDataChannel) OnOpen(handler func()) {
	d.dc.OnOpen(handler)
}

func (d *pionDataChannel) OnClose(handler func()) {
	d.dc.OnClose(handler)
}

func (d *pionDataChannel) OnMessage(handler func(pion.DataChannelMessage)) {
	d.dc.OnMessage(handler)
}

func (d *pionDataChannel) OnError(handler func(error)) {
	d.dc.OnError(handler)
}

func (d *pionDataChannel) Send(payload []byte) error {
	return d.dc.Send(payload)
}

func (d *pionDataChannel) Close() error {
	return d.dc.Close()
}
