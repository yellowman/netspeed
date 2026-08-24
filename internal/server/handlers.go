package server

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yellowman/netspeed/internal/meta"
	"github.com/yellowman/netspeed/internal/protocol"
)

// calculateSpeedMbps calculates speed in megabits per second from bytes and duration.
// Returns 0 if duration is zero to avoid division by zero.
func calculateSpeedMbps(bytes int64, duration time.Duration) float64 {
	if duration == 0 {
		return 0
	}
	// Convert bytes to bits, then to megabits
	bits := float64(bytes) * 8
	megabits := bits / 1_000_000
	// Convert duration to seconds
	seconds := duration.Seconds()
	return megabits / seconds
}

// formatSpeed returns a human-readable speed string.
func formatSpeed(mbps float64) string {
	if mbps >= 1000 {
		return fmt.Sprintf("%.2f Gbps", mbps/1000)
	}
	return fmt.Sprintf("%.2f Mbps", mbps)
}

// handleMeta handles GET /meta - returns client metadata.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientMeta := s.metaProvider.MetaFor(r)
	clientMeta.MaxTransferBytes = s.cfg.MaxBytes
	clientMeta.MeasurementProtocolVersion = protocol.MeasurementProtocolVersion
	clientMeta.UploadReceiptVersion = protocol.UploadReceiptVersion
	clientMeta.PacketLossFrameVersion = protocol.PacketLossFrameVersion

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	if err := json.NewEncoder(w).Encode(clientMeta); err != nil {
		// Log error but response is likely already started
		return
	}
}

// handleDown handles GET /__down - download/latency payload endpoint.
func (s *Server) handleDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	// Parse query parameters
	bytesStr := r.URL.Query().Get("bytes")
	measId := r.URL.Query().Get("measId")
	phase := r.URL.Query().Get("during") // "download", "upload", or empty for standalone

	var nBytes int64
	if bytesStr != "" {
		v, err := strconv.ParseInt(bytesStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid bytes parameter", http.StatusBadRequest)
			return
		}
		if v < 0 {
			http.Error(w, "bytes cannot be negative", http.StatusBadRequest)
			return
		}
		if v > s.cfg.MaxBytes {
			http.Error(w, "bytes exceeds maximum allowed", http.StatusBadRequest)
			return
		}
		nBytes = v
	}

	// Get client info for headers and logging
	clientMeta := s.metaProvider.MetaFor(r)
	clientIP := clientMeta.ClientIP

	// Set headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(nBytes, 10))
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.setMetaHeaders(w, clientMeta, start)

	// Set Server-Timing header before body starts (measures server-side latency)
	// Note: For streaming responses, this reflects setup time, not total transfer time
	s.setServerTiming(w, start)

	// If bytes == 0, this is a latency-only test (TTFB measurement)
	if nBytes == 0 {
		w.WriteHeader(http.StatusOK)
		latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
		if phase != "" {
			log.Printf("Latency probe: client=%s measId=%s phase=%s latency=%.3fms",
				clientIP, measId, phase, latencyMs)
		} else {
			log.Printf("Latency probe: client=%s measId=%s latency=%.3fms",
				clientIP, measId, latencyMs)
		}
		return
	}

	// Stream the payload
	buf := s.payloadBuf
	remaining := nBytes
	for remaining > 0 {
		chunk := int64(len(buf))
		if remaining < chunk {
			chunk = remaining
		}
		n, err := w.Write(buf[:chunk])
		if err != nil {
			// Client disconnected - log partial transfer
			duration := time.Since(start)
			bytesSent := nBytes - remaining + int64(n)
			speedMbps := calculateSpeedMbps(bytesSent, duration)
			log.Printf("Download interrupted: client=%s measId=%s bytes=%d/%d duration=%s speed=%s",
				clientIP, measId, bytesSent, nBytes, duration, formatSpeed(speedMbps))
			return
		}
		remaining -= int64(n)
	}

	// Log completed download with speed
	duration := time.Since(start)
	speedMbps := calculateSpeedMbps(nBytes, duration)
	log.Printf("Download: client=%s measId=%s bytes=%d duration=%s speed=%s",
		clientIP, measId, nBytes, duration, formatSpeed(speedMbps))
}

// handleUp handles POST /__up - upload sink endpoint.
func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()
	measID := r.URL.Query().Get("measId")
	clientIP := meta.ClientIPFromRequest(r, s.cfg.TrustProxyHeaders)

	n, err := protocol.ReadUpload(r.Body, r.ContentLength, s.cfg.MaxBytes)
	if err != nil {
		switch {
		case errors.Is(err, protocol.ErrUploadTooLarge):
			log.Printf("Upload rejected: client=%s measId=%s bytes=%d maxBytes=%d",
				clientIP, measID, n, s.cfg.MaxBytes)
			http.Error(w, protocol.ErrUploadTooLarge.Error(), http.StatusRequestEntityTooLarge)
		case errors.Is(err, protocol.ErrUploadLengthMismatch):
			log.Printf("Upload length mismatch: client=%s measId=%s bytes=%d contentLength=%d",
				clientIP, measID, n, r.ContentLength)
			http.Error(w, protocol.ErrUploadLengthMismatch.Error(), http.StatusBadRequest)
		default:
			log.Printf("Upload read error: client=%s measId=%s bytes=%d error=%v", clientIP, measID, n, err)
			http.Error(w, "failed to read complete upload", http.StatusBadRequest)
		}
		return
	}

	duration := time.Since(start)
	speedMbps := calculateSpeedMbps(n, duration)
	log.Printf("Upload: client=%s measId=%s bytes=%d duration=%s speed=%s",
		clientIP, measID, n, duration, formatSpeed(speedMbps))

	receipt := protocol.UploadReceipt{
		OK:               true,
		AcceptedBytes:    n,
		ServerDurationNS: duration.Nanoseconds(),
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.setServerTiming(w, start)
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(receipt); err != nil {
		log.Printf("Upload receipt write error: client=%s measId=%s error=%v", clientIP, measID, err)
	}
}

// handleLocations handles GET /locations - returns list of test locations.
func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	locs := s.locations.All()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")

	if err := json.NewEncoder(w).Encode(locs); err != nil {
		return
	}
}

// handleTrace handles GET /cdn-cgi/trace - optional diagnostic endpoint.
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientMeta := s.metaProvider.MetaFor(r)
	tlsVersion := getTLSVersion(r)
	httpVersion := getHTTPVersion(r)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	fmt.Fprintf(w, "ip=%s\n", clientMeta.ClientIP)
	fmt.Fprintf(w, "tls=%s\n", tlsVersion)
	fmt.Fprintf(w, "http=%s\n", httpVersion)
	fmt.Fprintf(w, "colo=%s\n", clientMeta.Colo)
	fmt.Fprintf(w, "loc=%s\n", clientMeta.Country)
	fmt.Fprintf(w, "city=%s\n", clientMeta.City)
	fmt.Fprintf(w, "region=%s\n", clientMeta.Region)
	fmt.Fprintf(w, "asn=%d\n", clientMeta.ASN)
	fmt.Fprintf(w, "asorg=%s\n", clientMeta.ASOrg)
}

// TurnCredentialsResponse is the response for /api/turn/credentials.
type TurnCredentialsResponse struct {
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTLSec     int64    `json:"ttlSec"`
	Servers    []string `json:"servers"`
	Realm      string   `json:"realm"`
}

// handleTurnCredentials handles GET /api/turn/credentials.
func (s *Server) handleTurnCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Determine TURN servers - either configured or derived from embedded TURN
	var turnServers []string
	if len(s.cfg.TurnServers) > 0 {
		turnServers = s.cfg.TurnServers
	} else if s.cfg.EmbeddedTurnPort != "" && s.cfg.TurnSecret != "" {
		// Derive TURN server URL from request host
		host := r.Host
		// Strip port from host if present, handling IPv6 addresses properly
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		// RFC 7065/3986: IPv6 addresses must be enclosed in brackets in URIs
		// net.SplitHostPort strips brackets, so we need to re-add them for IPv6
		// But if SplitHostPort failed (no port), brackets may already be present
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		// Include both STUN and TURN URLs - browsers need STUN for reflexive candidates
		turnServers = []string{
			fmt.Sprintf("stun:%s:%s", host, s.cfg.EmbeddedTurnPort),
			fmt.Sprintf("turn:%s:%s?transport=udp", host, s.cfg.EmbeddedTurnPort),
		}
	}

	// Check if TURN is configured
	if s.cfg.TurnSecret == "" || len(turnServers) == 0 {
		http.Error(w, "TURN not configured", http.StatusServiceUnavailable)
		return
	}

	// Parse optional TTL parameter
	ttlStr := r.URL.Query().Get("ttl")
	var ttl int64 = 600 // Default 10 minutes
	if ttlStr != "" {
		if v, err := strconv.ParseInt(ttlStr, 10, 64); err == nil && v > 0 {
			ttl = v
		}
	}

	// Clamp TTL
	if ttl < 60 {
		ttl = 60
	}
	if ttl > s.cfg.MaxTurnTTL {
		ttl = s.cfg.MaxTurnTTL
	}

	// Compute expiry and generate username
	now := time.Now().Unix()
	exp := now + ttl

	// Generate a simple token (could be session-based)
	token := fmt.Sprintf("%x", now)
	username := fmt.Sprintf("%d:%s", exp, token)

	// Compute HMAC-SHA1 credential
	mac := hmac.New(sha1.New, []byte(s.cfg.TurnSecret))
	mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	resp := TurnCredentialsResponse{
		Username:   username,
		Credential: credential,
		TTLSec:     ttl,
		Servers:    turnServers,
		Realm:      s.cfg.TurnRealm,
	}

	log.Printf("TURN credentials: servers=%v username=%s realm=%s", turnServers, username, s.cfg.TurnRealm)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	json.NewEncoder(w).Encode(resp)
}

// PacketTestOfferRequest is the request body for /api/packet-test/offer.
type PacketTestOfferRequest struct {
	SDP         string `json:"sdp"`
	Type        string `json:"type"`
	TestProfile string `json:"testProfile,omitempty"`
}

// PacketTestOfferResponse is the response for /api/packet-test/offer.
type PacketTestOfferResponse struct {
	SDP    string `json:"sdp"`
	Type   string `json:"type"`
	TestID string `json:"testId"`
}

// handlePacketTestOffer handles POST /api/packet-test/offer.
// This endpoint performs WebRTC signaling for packet loss testing.
func (s *Server) handlePacketTestOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if WebRTC manager is available
	if s.webrtcManager == nil {
		http.Error(w, "WebRTC not available", http.StatusServiceUnavailable)
		return
	}

	// Parse request
	var req PacketTestOfferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Type != "offer" {
		http.Error(w, "type must be 'offer'", http.StatusBadRequest)
		return
	}

	if req.SDP == "" {
		http.Error(w, "sdp is required", http.StatusBadRequest)
		return
	}

	// Handle the offer and get an answer
	answerSDP, testID, err := s.webrtcManager.HandleOffer(req.SDP, req.TestProfile)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to process offer: %v", err), http.StatusInternalServerError)
		return
	}

	resp := PacketTestOfferResponse{
		SDP:    answerSDP,
		Type:   "answer",
		TestID: testID,
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	json.NewEncoder(w).Encode(resp)
}

// PacketTestReportRequest is the request body for /api/packet-test/report.
type PacketTestReportRequest struct {
	TestID            string  `json:"testId"`
	Sent              int     `json:"sent"`
	Received          int     `json:"received"`
	LossPercent       float64 `json:"lossPercent"`
	RTTMin            float64 `json:"rttMinMs"`
	RTTMedian         float64 `json:"rttMedianMs"`
	RTTP90            float64 `json:"rttP90Ms"`
	JitterMs          float64 `json:"jitterMs"`
	TurnServer        string  `json:"turnServer,omitempty"`
	TransportProtocol string  `json:"transportProtocol,omitempty"`
}

// PacketTestReportResponse contains authoritative server-side counters. These
// counters let clients distinguish forward-path probe loss from reverse-path
// acknowledgement loss and round-trip transaction loss.
type PacketTestReportResponse struct {
	OK                   bool `json:"ok"`
	ProtocolVersion      int  `json:"protocolVersion"`
	FrameSizeBytes       int  `json:"frameSizeBytes"`
	ForwardReceived      int  `json:"forwardReceived"`
	AcknowledgementsSent int  `json:"acknowledgementsSent"`
	DuplicateFrames      int  `json:"duplicateFrames"`
	InvalidFrames        int  `json:"invalidFrames"`
	AckSendFailures      int  `json:"ackSendFailures"`
}

// handlePacketTestReport handles POST /api/packet-test/report.
// This endpoint receives packet loss test results from the client.
func (s *Server) handlePacketTestReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PacketTestReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TestID == "" {
		http.Error(w, "testId is required", http.StatusBadRequest)
		return
	}
	if s.webrtcManager == nil {
		http.Error(w, "WebRTC not available", http.StatusServiceUnavailable)
		return
	}

	snapshot, ok := s.webrtcManager.PacketLossSnapshot(req.TestID)
	if !ok {
		http.Error(w, "packet test session not found", http.StatusNotFound)
		return
	}

	clientIP := meta.ClientIPFromRequest(r, s.cfg.TrustProxyHeaders)
	log.Printf("Packet test report: testId=%s client=%s sent=%d transactionReceived=%d clientLoss=%.2f%% forwardReceived=%d acksSent=%d invalid=%d duplicates=%d ackSendFailures=%d rtt=[%.2f/%.2f/%.2f]ms jitter=%.2fms",
		req.TestID, clientIP, req.Sent, req.Received, req.LossPercent,
		snapshot.ForwardReceived, snapshot.AcknowledgementsSent, snapshot.InvalidFrames,
		snapshot.DuplicateFrames, snapshot.AckSendFailures,
		req.RTTMin, req.RTTMedian, req.RTTP90, req.JitterMs)

	response := PacketTestReportResponse{
		OK:                   true,
		ProtocolVersion:      protocol.MeasurementProtocolVersion,
		FrameSizeBytes:       snapshot.FrameSizeBytes,
		ForwardReceived:      snapshot.ForwardReceived,
		AcknowledgementsSent: snapshot.AcknowledgementsSent,
		DuplicateFrames:      snapshot.DuplicateFrames,
		InvalidFrames:        snapshot.InvalidFrames,
		AckSendFailures:      snapshot.AckSendFailures,
	}

	// Snapshot first, then close. Closing before the snapshot would erase the
	// only authoritative record of which forward probes reached the server.
	s.webrtcManager.CloseSession(req.TestID)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Packet test report response write error: testId=%s error=%v", req.TestID, err)
	}
}
