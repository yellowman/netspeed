// Package client provides the speed test client implementation.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"

	"github.com/yellowman/netspeed/internal/measurement"
	"github.com/yellowman/netspeed/internal/protocol"
)

// TURN credentials from server.
type turnCredentials struct {
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTLSec     int      `json:"ttlSec"`
	Servers    []string `json:"servers"`
}

// Signaling request/response.
type signalingRequest struct {
	SDP         string `json:"sdp"`
	Type        string `json:"type"`
	TestProfile string `json:"testProfile"`
}

type signalingResponse struct {
	SDP    string `json:"sdp"`
	Type   string `json:"type"`
	TestID string `json:"testId"`
}

type packetTestReportRequest struct {
	TestID      string  `json:"testId"`
	Sent        int     `json:"sent"`
	Received    int     `json:"received"`
	LossPercent float64 `json:"lossPercent"`
	RTTMin      float64 `json:"rttMinMs"`
	RTTMedian   float64 `json:"rttMedianMs"`
	RTTP90      float64 `json:"rttP90Ms"`
	JitterMs    float64 `json:"jitterMs"`
}

type packetTestReportResponse struct {
	OK                   bool `json:"ok"`
	ProtocolVersion      int  `json:"protocolVersion"`
	FrameSizeBytes       int  `json:"frameSizeBytes"`
	ForwardReceived      int  `json:"forwardReceived"`
	AcknowledgementsSent int  `json:"acknowledgementsSent"`
	DuplicateFrames      int  `json:"duplicateFrames"`
	InvalidFrames        int  `json:"invalidFrames"`
	AckSendFailures      int  `json:"ackSendFailures"`
}

// fetchTURNCredentials gets TURN credentials from the server.
func (c *Client) fetchTURNCredentials(ctx context.Context) (*turnCredentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.ServerURL+"/api/turn/credentials", nil)
	if err != nil {
		return nil, err
	}
	c.setRequestHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch TURN credentials: %w", err)
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("TURN credentials request: %w", err)
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(contentType, "application/json") {
		return nil, fmt.Errorf("TURN credentials returned unexpected content type %q", contentType)
	}

	var credentials turnCredentials
	if err := decodeLimitedJSON(resp.Body, maxMetaBodyBytes, &credentials); err != nil {
		return nil, fmt.Errorf("decode TURN credentials: %w", err)
	}
	return &credentials, nil
}

// exchangeOffer sends an SDP offer and receives the answer.
func (c *Client) exchangeOffer(ctx context.Context, offerSDP string) (*signalingResponse, error) {
	body, err := json.Marshal(signalingRequest{
		SDP:         offerSDP,
		Type:        "offer",
		TestProfile: "loss-exact-v1",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.cfg.ServerURL+"/api/packet-test/offer",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	c.setRequestHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signaling request: %w", err)
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return nil, fmt.Errorf("signaling: %w", err)
	}

	var signaling signalingResponse
	if err := decodeLimitedJSON(resp.Body, maxMetaBodyBytes, &signaling); err != nil {
		return nil, fmt.Errorf("decode signaling response: %w", err)
	}
	return &signaling, nil
}

func (c *Client) reportPacketTest(
	ctx context.Context,
	request packetTestReportRequest,
) (packetTestReportResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return packetTestReportResponse{}, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.cfg.ServerURL+"/api/packet-test/report",
		bytes.NewReader(body),
	)
	if err != nil {
		return packetTestReportResponse{}, err
	}
	c.setRequestHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return packetTestReportResponse{}, fmt.Errorf("packet report request: %w", err)
	}
	defer resp.Body.Close()
	if err := requireMeasurementStatus(resp, http.StatusOK); err != nil {
		return packetTestReportResponse{}, fmt.Errorf("packet report: %w", err)
	}
	if contentType := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(contentType, "application/json") {
		return packetTestReportResponse{}, fmt.Errorf("packet report returned unexpected content type %q", contentType)
	}

	var report packetTestReportResponse
	if err := decodeLimitedJSON(resp.Body, maxPacketReportBodyBytes, &report); err != nil {
		return packetTestReportResponse{}, fmt.Errorf("decode packet report: %w", err)
	}
	if !report.OK {
		return packetTestReportResponse{}, fmt.Errorf("server rejected packet report")
	}
	if report.ProtocolVersion < protocol.MeasurementProtocolVersion {
		return packetTestReportResponse{}, fmt.Errorf("packet report protocol %d; need %d",
			report.ProtocolVersion, protocol.MeasurementProtocolVersion)
	}
	if report.FrameSizeBytes != protocol.PacketFrameSize {
		return packetTestReportResponse{}, fmt.Errorf("packet report frame size %d; need %d",
			report.FrameSizeBytes, protocol.PacketFrameSize)
	}
	return report, nil
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func boundedCount(value, maximum int) int {
	if value < 0 {
		return 0
	}
	if value > maximum {
		return maximum
	}
	return value
}

func calculateLossPercent(sent, received int) float64 {
	if sent <= 0 {
		return 0
	}
	received = boundedCount(received, sent)
	return float64(sent-received) / float64(sent) * 100
}

func validatePacketReport(report packetTestReportResponse, probesSent, acknowledgementsReceived int) error {
	if probesSent <= 0 {
		return fmt.Errorf("packet report has no submitted probes")
	}
	if report.ForwardReceived < 0 || report.ForwardReceived > probesSent {
		return fmt.Errorf("forwardReceived %d is outside 0..%d", report.ForwardReceived, probesSent)
	}
	if report.AcknowledgementsSent < 0 || report.AcknowledgementsSent > report.ForwardReceived {
		return fmt.Errorf("acknowledgementsSent %d is outside 0..%d", report.AcknowledgementsSent, report.ForwardReceived)
	}
	if report.AckSendFailures < 0 || report.AckSendFailures > report.ForwardReceived {
		return fmt.Errorf("ackSendFailures %d is outside 0..%d", report.AckSendFailures, report.ForwardReceived)
	}
	if report.AcknowledgementsSent+report.AckSendFailures != report.ForwardReceived {
		return fmt.Errorf("daemon acknowledgement accounting is inconsistent: sent=%d failures=%d forwardReceived=%d",
			report.AcknowledgementsSent, report.AckSendFailures, report.ForwardReceived)
	}
	if acknowledgementsReceived < 0 || acknowledgementsReceived > report.AcknowledgementsSent {
		return fmt.Errorf("client received %d acknowledgements but daemon sent %d",
			acknowledgementsReceived, report.AcknowledgementsSent)
	}
	if report.DuplicateFrames < 0 || report.InvalidFrames < 0 {
		return fmt.Errorf("daemon returned negative packet counters")
	}
	return nil
}

func containsTURNURL(servers []string) bool {
	for _, server := range servers {
		lower := strings.ToLower(strings.TrimSpace(server))
		if strings.HasPrefix(lower, "turn:") || strings.HasPrefix(lower, "turns:") {
			return true
		}
	}
	return false
}

// runPacketLossTestWebRTC runs the exact-size WebRTC packet loss test.
func (c *Client) runPacketLossTestWebRTC(ctx context.Context) (*PacketLossResult, error) {
	if c.packetLossFrameVersion < protocol.PacketLossFrameVersion {
		return &PacketLossResult{
			Unavailable: true,
			Reason: fmt.Sprintf("server packet-loss frame version %d; need %d",
				c.packetLossFrameVersion, protocol.PacketLossFrameVersion),
		}, nil
	}

	credentials, err := c.fetchTURNCredentials(ctx)
	if err != nil {
		return &PacketLossResult{Unavailable: true, Reason: fmt.Sprintf("TURN unavailable: %v", err)}, nil
	}
	if !containsTURNURL(credentials.Servers) {
		return &PacketLossResult{Unavailable: true, Reason: "TURN relay is not configured"}, nil
	}

	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{
			URLs:       credentials.Servers,
			Username:   credentials.Username,
			Credential: credentials.Credential,
		}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}
	defer peerConnection.Close()

	ordered := false
	maxRetransmits := uint16(0)
	dataChannel, err := peerConnection.CreateDataChannel("packet-loss", &webrtc.DataChannelInit{
		Ordered:        &ordered,
		MaxRetransmits: &maxRetransmits,
	})
	if err != nil {
		return nil, fmt.Errorf("create packet-loss data channel: %w", err)
	}

	sendTimes := make(map[uint32]time.Time)
	receiveTimes := make(map[uint32]time.Time)
	acknowledgements := make(map[uint32]protocol.PacketFrame)
	var packetMu sync.Mutex
	dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		receivedAt := time.Now()
		frame, err := protocol.DecodePacketFrame(message.Data)
		if err != nil || !frame.Acknowledgement {
			return
		}
		packetMu.Lock()
		if _, submitted := sendTimes[frame.Sequence]; !submitted {
			packetMu.Unlock()
			return
		}
		if _, duplicate := acknowledgements[frame.Sequence]; duplicate {
			packetMu.Unlock()
			return
		}
		acknowledgements[frame.Sequence] = frame
		receiveTimes[frame.Sequence] = receivedAt
		packetMu.Unlock()
	})

	// Register lifecycle callbacks before negotiation. A fast local/relay path
	// can open the data channel immediately after SetRemoteDescription; adding
	// OnOpen afterward risks missing the only notification and timing out a
	// healthy packet test.
	opened := make(chan struct{})
	var openOnce sync.Once
	dataChannel.OnOpen(func() { openOnce.Do(func() { close(opened) }) })
	iceFailed := make(chan struct{})
	var failureOnce sync.Once
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateDisconnected {
			failureOnce.Do(func() { close(iceFailed) })
		}
	})

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("create offer: %w", err)
	}
	if err := peerConnection.SetLocalDescription(offer); err != nil {
		return nil, fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(peerConnection):
	case <-time.After(10 * time.Second):
		return &PacketLossResult{Unavailable: true, Reason: "ICE gathering timeout"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	signaling, err := c.exchangeOffer(ctx, peerConnection.LocalDescription().SDP)
	if err != nil {
		return &PacketLossResult{Unavailable: true, Reason: fmt.Sprintf("signaling failed: %v", err)}, nil
	}
	if err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  signaling.SDP,
	}); err != nil {
		return nil, fmt.Errorf("set remote description: %w", err)
	}

	select {
	case <-opened:
	case <-iceFailed:
		return &PacketLossResult{Unavailable: true, Reason: "ICE connection failed"}, nil
	case <-time.After(15 * time.Second):
		return &PacketLossResult{Unavailable: true, Reason: "ICE connection timeout"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	const packetCount = 1000
	const packetInterval = 10 * time.Millisecond
	actualSent := 0
sendLoop:
	for sequence := 0; sequence < packetCount; sequence++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-iceFailed:
			break sendLoop
		default:
		}

		sentAt := time.Now()
		// Sequence numbers are contiguous across successfully submitted probes.
		// A local Send failure is not an on-wire loss and must not leave a hole
		// that later pattern analysis mistakes for network packet loss.
		sequenceNumber := uint32(actualSent)
		packetMu.Lock()
		sendTimes[sequenceNumber] = sentAt
		packetMu.Unlock()
		if err := dataChannel.Send(protocol.EncodeProbeFrame(sequenceNumber, sentAt.UnixMilli())); err != nil {
			packetMu.Lock()
			delete(sendTimes, sequenceNumber)
			packetMu.Unlock()
			continue
		}
		actualSent++
		if c.cfg.OnProgress != nil {
			c.cfg.OnProgress("packet-loss", sequence+1, packetCount, 0)
		}
		if err := waitForContext(ctx, packetInterval); err != nil {
			return nil, err
		}
	}

	if actualSent == 0 {
		return &PacketLossResult{Unavailable: true, Reason: "no exact-size packet probes were sent"}, nil
	}
	if err := waitForContext(ctx, 3*time.Second); err != nil {
		return nil, err
	}

	packetMu.Lock()
	ackCount := len(acknowledgements)
	roundTrips := make([]float64, 0, ackCount)
	for sequence := range acknowledgements {
		sentAt, sentOK := sendTimes[sequence]
		receivedAt, receivedOK := receiveTimes[sequence]
		if sentOK && receivedOK {
			rttMilliseconds := float64(receivedAt.Sub(sentAt).Microseconds()) / 1000
			if rttMilliseconds > 0 && rttMilliseconds < 30_000 {
				roundTrips = append(roundTrips, rttMilliseconds)
			}
		}
	}
	packetMu.Unlock()

	transactionLoss := calculateLossPercent(actualSent, ackCount)
	rttStats := RTTStats{}
	jitter := 0.0
	if len(roundTrips) > 0 {
		rttStats = RTTStats{
			Min:    measurement.Percentile(roundTrips, 0),
			Median: measurement.Percentile(roundTrips, 50),
			P90:    measurement.Percentile(roundTrips, 90),
		}
		jitter = measurement.Jitter(roundTrips)
	}

	report, err := c.reportPacketTest(ctx, packetTestReportRequest{
		TestID:      signaling.TestID,
		Sent:        actualSent,
		Received:    ackCount,
		LossPercent: transactionLoss,
		RTTMin:      rttStats.Min,
		RTTMedian:   rttStats.Median,
		RTTP90:      rttStats.P90,
		JitterMs:    jitter,
	})
	if err != nil {
		return &PacketLossResult{
			Sent:        actualSent,
			Received:    ackCount,
			Unavailable: true,
			Reason:      fmt.Sprintf("server packet counters unavailable: %v", err),
			TestID:      signaling.TestID,
		}, nil
	}

	if err := validatePacketReport(report, actualSent, ackCount); err != nil {
		return &PacketLossResult{
			Sent:        actualSent,
			Received:    ackCount,
			Unavailable: true,
			Reason:      fmt.Sprintf("invalid daemon packet counters: %v", err),
			TestID:      signaling.TestID,
		}, nil
	}

	forwardReceived := report.ForwardReceived
	acknowledgementsSent := report.AcknowledgementsSent
	acknowledgementsReceived := ackCount
	forwardLoss := calculateLossPercent(actualSent, forwardReceived)
	forwardLossPointer := &forwardLoss
	var reverseLossPointer *float64
	if acknowledgementsSent > 0 {
		reverseLoss := calculateLossPercent(acknowledgementsSent, acknowledgementsReceived)
		reverseLossPointer = &reverseLoss
	}

	return &PacketLossResult{
		Sent:                              actualSent,
		Received:                          ackCount,
		LossPercent:                       transactionLoss,
		TransactionLossPercent:            transactionLoss,
		ForwardSent:                       actualSent,
		ForwardReceived:                   forwardReceived,
		ForwardLossPercent:                forwardLossPointer,
		AcknowledgementsSent:              acknowledgementsSent,
		AcknowledgementsReceived:          acknowledgementsReceived,
		ReverseAcknowledgementLossPercent: reverseLossPointer,
		FrameSizeBytes:                    report.FrameSizeBytes,
		DuplicateFrames:                   report.DuplicateFrames,
		InvalidFrames:                     report.InvalidFrames,
		AckSendFailures:                   report.AckSendFailures,
		RTTStatsMs:                        rttStats,
		JitterMs:                          jitter,
		TestID:                            signaling.TestID,
	}, nil
}
