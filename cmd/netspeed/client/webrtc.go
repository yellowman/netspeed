// Package client provides the speed test client implementation.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
)

// TURN credentials from server
type turnCredentials struct {
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTLSec     int      `json:"ttlSec"`
	Servers    []string `json:"servers"`
}

// Packet message format (sent by client)
type packetMessage struct {
	Seq    int   `json:"seq"`
	SentAt int64 `json:"sentAt"`
}

// Ack message format (received from server)
type ackMessage struct {
	Seq    int   `json:"seq"`
	SentAt int64 `json:"sentAt"`
	RecvAt int64 `json:"recvAt"`
}

// Signaling request/response
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

// fetchTURNCredentials gets TURN credentials from the server.
func (c *Client) fetchTURNCredentials(ctx context.Context) (*turnCredentials, error) {
	url := c.cfg.ServerURL + "/api/turn/credentials"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch TURN credentials: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TURN credentials request failed: %d", resp.StatusCode)
	}

	var creds turnCredentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return nil, fmt.Errorf("failed to decode TURN credentials: %w", err)
	}

	return &creds, nil
}

// exchangeOffer sends SDP offer and receives answer.
func (c *Client) exchangeOffer(ctx context.Context, offerSDP string) (*signalingResponse, error) {
	url := c.cfg.ServerURL + "/api/packet-test/offer"

	reqBody := signalingRequest{
		SDP:         offerSDP,
		Type:        "offer",
		TestProfile: "loss-basic",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, io.NopCloser(
		&bytesReader{data: body},
	))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signaling request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signaling failed: %d", resp.StatusCode)
	}

	var sigResp signalingResponse
	if err := json.NewDecoder(resp.Body).Decode(&sigResp); err != nil {
		return nil, fmt.Errorf("failed to decode signaling response: %w", err)
	}

	return &sigResp, nil
}

// bytesReader is a simple io.Reader for bytes.
type bytesReader struct {
	data []byte
	pos  int
}

func (r *bytesReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// runPacketLossTestWebRTC runs the full WebRTC packet loss test.
func (c *Client) runPacketLossTestWebRTC(ctx context.Context) (*PacketLossResult, error) {
	// Step 1: Fetch TURN credentials
	creds, err := c.fetchTURNCredentials(ctx)
	if err != nil {
		return &PacketLossResult{
			Unavailable: true,
			Reason:      fmt.Sprintf("TURN unavailable: %v", err),
		}, nil
	}

	// Step 2: Create peer connection with TURN
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{
			URLs:       creds.Servers,
			Username:   creds.Username,
			Credential: creds.Credential,
		}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay, // Force TURN
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer connection: %w", err)
	}
	defer pc.Close()

	// Step 3: Create unreliable, unordered data channel for packet loss test
	ordered := false
	maxRetransmits := uint16(0)
	dc, err := pc.CreateDataChannel("packet-loss", &webrtc.DataChannelInit{
		Ordered:        &ordered,
		MaxRetransmits: &maxRetransmits,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create data channel: %w", err)
	}

	// Step 4: Set up ack collection
	result := &PacketLossResult{}
	sendTimes := make(map[int]time.Time)
	acks := make(map[int]ackMessage)
	var ackMu sync.Mutex

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		var ack ackMessage
		if err := json.Unmarshal(msg.Data, &ack); err == nil {
			ackMu.Lock()
			acks[ack.Seq] = ack
			ackMu.Unlock()
		}
	})

	// Step 5: Create offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create offer: %w", err)
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		return nil, fmt.Errorf("failed to set local description: %w", err)
	}

	// Wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gatherComplete:
	case <-time.After(10 * time.Second):
		return &PacketLossResult{
			Unavailable: true,
			Reason:      "ICE gathering timeout",
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Step 6: Exchange offer/answer
	sigResp, err := c.exchangeOffer(ctx, pc.LocalDescription().SDP)
	if err != nil {
		return &PacketLossResult{
			Unavailable: true,
			Reason:      fmt.Sprintf("Signaling failed: %v", err),
		}, nil
	}

	result.TestID = sigResp.TestID

	// Set remote description
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sigResp.SDP,
	}); err != nil {
		return nil, fmt.Errorf("failed to set remote description: %w", err)
	}

	// Step 7: Wait for data channel to open
	openCh := make(chan struct{})
	dc.OnOpen(func() {
		close(openCh)
	})

	// Monitor ICE connection state
	iceFailed := make(chan struct{})
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateFailed ||
			state == webrtc.ICEConnectionStateDisconnected {
			select {
			case <-iceFailed:
			default:
				close(iceFailed)
			}
		}
	})

	select {
	case <-openCh:
		// Data channel opened successfully
	case <-iceFailed:
		return &PacketLossResult{
			Unavailable: true,
			Reason:      "ICE connection failed",
		}, nil
	case <-time.After(15 * time.Second):
		return &PacketLossResult{
			Unavailable: true,
			Reason:      "ICE connection timeout",
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Step 8: Send packets
	const numPackets = 1000
	const intervalMs = 10

	for seq := 0; seq < numPackets; seq++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-iceFailed:
			// Connection died mid-test
			ackMu.Lock()
			received := len(acks)
			ackMu.Unlock()
			if received == 0 {
				return &PacketLossResult{
					Unavailable: true,
					Reason:      "Connection died before receiving responses",
				}, nil
			}
			// Continue to calculate partial results
			goto calculateResults
		default:
		}

		msg := packetMessage{
			Seq:    seq,
			SentAt: time.Now().UnixMilli(),
		}
		data, _ := json.Marshal(msg)

		if err := dc.Send(data); err != nil {
			continue
		}
		sendTimes[seq] = time.Now()

		if c.cfg.OnProgress != nil {
			c.cfg.OnProgress("packet-loss", seq+1, numPackets, 0)
		}

		time.Sleep(time.Duration(intervalMs) * time.Millisecond)
	}

	// Step 9: Wait for late acks (3 seconds)
	time.Sleep(3 * time.Second)

calculateResults:
	// Step 10: Calculate results
	ackMu.Lock()
	defer ackMu.Unlock()

	result.Sent = numPackets
	result.Received = len(acks)
	result.LossPercent = float64(numPackets-len(acks)) / float64(numPackets) * 100

	// Calculate RTT statistics
	var rtts []float64
	for seq, ack := range acks {
		if sendTime, ok := sendTimes[seq]; ok {
			// Calculate RTT from local send time to local receive time of ack
			rtt := float64(time.Now().UnixMilli()-ack.SentAt) / 2 // Approximate
			// Better: use the difference between send and receive at server
			if ack.RecvAt > 0 {
				// Server-measured RTT approximation
				rtt = float64(ack.RecvAt-ack.SentAt) * 2
			}
			// Even better: use actual round-trip
			actualRTT := float64(time.Since(sendTime).Milliseconds())
			if actualRTT > 0 && actualRTT < 30000 { // Sanity check
				rtts = append(rtts, actualRTT)
			} else if rtt > 0 {
				rtts = append(rtts, rtt)
			}
		}
	}

	if len(rtts) > 0 {
		sort.Float64s(rtts)
		result.RTTStatsMs = RTTStats{
			Min:    rtts[0],
			Median: percentile(rtts, 50),
			P90:    percentile(rtts, 90),
		}
		result.JitterMs = result.RTTStatsMs.P90 - result.RTTStatsMs.Median
	}

	// Detect connection failure vs actual packet loss
	if result.Received == 0 {
		return &PacketLossResult{
			Unavailable: true,
			Reason:      "No responses received - connection failed",
		}, nil
	}

	// Check for connection dying mid-test
	maxAckedSeq := 0
	for seq := range acks {
		if seq > maxAckedSeq {
			maxAckedSeq = seq
		}
	}

	// If we received less than 10% and max acked is less than 50% through,
	// this looks like a connection failure
	if result.Received < numPackets/10 && maxAckedSeq < numPackets/2 {
		return &PacketLossResult{
			Unavailable: true,
			Reason:      fmt.Sprintf("Connection died mid-test - last response at packet %d/%d", maxAckedSeq, numPackets),
		}, nil
	}

	return result, nil
}
