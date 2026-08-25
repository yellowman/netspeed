package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yellowman/netspeed/internal/protocol"
	"github.com/yellowman/netspeed/internal/webrtc"
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
	clientMeta.MaxConcurrentTransfersPerClient = s.cfg.MaxConcurrentTransfersPerClient
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

	// Get client info for headers, admission, quota, and logging.
	clientMeta := s.metaProvider.MetaFor(r)
	clientIP := clientMeta.ClientIP
	release, admitted := s.beginTransfer(w, r)
	if !admitted {
		return
	}
	defer release()
	if !s.reserveBandwidth(w, clientIP, nBytes) {
		return
	}

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
		if n > 0 {
			s.metrics.downloadBytes.Add(uint64(n))
		}
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
	clientIP := s.clientIP(r)
	release, admitted := s.beginTransfer(w, r)
	if !admitted {
		return
	}
	defer release()

	bodyReader := io.Reader(r.Body)
	quotaReserved := false
	if r.ContentLength >= 0 && r.ContentLength <= s.cfg.MaxBytes {
		if !s.reserveBandwidth(w, clientIP, r.ContentLength) {
			return
		}
		quotaReserved = true
	}
	if !quotaReserved {
		bodyReader = &quotaChargingReader{reader: r.Body, quota: s.bandwidthQuota, key: clientIP}
	}

	n, err := protocol.ReadUpload(bodyReader, r.ContentLength, s.cfg.MaxBytes)
	if n > 0 {
		s.metrics.uploadBytes.Add(uint64(n))
	}
	if err != nil {
		switch {
		case errors.Is(err, errBandwidthQuotaExceeded):
			s.metrics.bandwidthQuotaRejected.Add(1)
			var quotaErr *bandwidthQuotaError
			if errors.As(err, &quotaErr) {
				setRetryAfter(w, quotaErr.retryAfter)
			}
			log.Printf("Upload quota rejected: client=%s measId=%s bytes=%d", clientIP, measID, n)
			http.Error(w, errBandwidthQuotaExceeded.Error(), http.StatusTooManyRequests)
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
		s.metrics.internalFailures.Add(1)
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

	clientKey := s.clientIP(r)
	if allowed, retryAfter := s.turnCredentialLimiter.Allow(clientKey); !allowed {
		s.metrics.turnCredentialRateRejected.Add(1)
		rejectRateLimited(w, retryAfter, "TURN credential request rate exceeded")
		return
	}
	if len(s.cfg.TurnServers) == 0 {
		http.Error(w, "ICE servers not configured", http.StatusServiceUnavailable)
		return
	}
	hasTURN := false
	for _, raw := range s.cfg.TurnServers {
		lower := strings.ToLower(strings.TrimSpace(raw))
		if strings.HasPrefix(lower, "turn:") || strings.HasPrefix(lower, "turns:") {
			hasTURN = true
			break
		}
	}
	if hasTURN && s.cfg.TurnSecret == "" {
		http.Error(w, "TURN credentials are not configured", http.StatusServiceUnavailable)
		return
	}

	ttl := int64(600)
	if ttlStr := r.URL.Query().Get("ttl"); ttlStr != "" {
		if parsed, err := strconv.ParseInt(ttlStr, 10, 64); err == nil && parsed > 0 {
			ttl = parsed
		}
	}
	if ttl < 60 {
		ttl = 60
	}
	if ttl > s.cfg.MaxTurnTTL {
		ttl = s.cfg.MaxTurnTTL
	}

	response := TurnCredentialsResponse{
		Servers: append([]string(nil), s.cfg.TurnServers...),
		Realm:   s.cfg.TurnRealm,
	}
	if hasTURN {
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			s.metrics.internalFailures.Add(1)
			http.Error(w, "failed to generate TURN credential", http.StatusInternalServerError)
			return
		}
		expiry := time.Now().Unix() + ttl
		response.Username = fmt.Sprintf("%d:%s", expiry, hex.EncodeToString(tokenBytes))
		mac := hmac.New(sha1.New, []byte(s.cfg.TurnSecret))
		_, _ = mac.Write([]byte(response.Username))
		response.Credential = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		response.TTLSec = ttl
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.metrics.internalFailures.Add(1)
		log.Printf("TURN credential response write error: client=%s error=%v", clientKey, err)
		return
	}
	s.metrics.turnCredentialsIssued.Add(1)
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
	if s.webrtcManager == nil {
		http.Error(w, "WebRTC not available", http.StatusServiceUnavailable)
		return
	}

	clientKey := s.clientIP(r)
	if allowed, retryAfter := s.offerRateLimiter.Allow(clientKey); !allowed {
		s.metrics.webrtcOfferRateRejected.Add(1)
		rejectRateLimited(w, retryAfter, "WebRTC offer rate exceeded")
		return
	}

	var request PacketTestOfferRequest
	if !s.decodeControlJSON(w, r, s.cfg.MaxOfferBodyBytes, &request) {
		return
	}
	if request.Type != "offer" {
		http.Error(w, "type must be 'offer'", http.StatusBadRequest)
		return
	}
	if request.SDP == "" {
		http.Error(w, "sdp is required", http.StatusBadRequest)
		return
	}

	answerSDP, testID, err := s.webrtcManager.HandleOfferForClient(r.Context(), request.SDP, request.TestProfile, clientKey)
	if err != nil {
		switch {
		case errors.Is(err, webrtc.ErrClientSessionCapacity):
			s.metrics.webrtcCapacityRejected.Add(1)
			rejectRateLimited(w, time.Second, err.Error())
		case errors.Is(err, webrtc.ErrSessionCapacity), errors.Is(err, webrtc.ErrManagerClosed):
			s.metrics.webrtcCapacityRejected.Add(1)
			w.Header().Set("Retry-After", "1")
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		default:
			s.metrics.internalFailures.Add(1)
			log.Printf("Packet-test offer failed: client=%s error=%v", clientKey, err)
			http.Error(w, "failed to process offer", http.StatusInternalServerError)
		}
		return
	}

	s.metrics.webrtcOffers.Add(1)
	response := PacketTestOfferResponse{SDP: answerSDP, Type: "answer", TestID: testID}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.metrics.internalFailures.Add(1)
		log.Printf("Packet-test offer response write error: client=%s testId=%s error=%v", clientKey, testID, err)
	}
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

func validatePacketTestReport(report PacketTestReportRequest) error {
	if report.Sent < 1 || report.Sent > 100_000 {
		return fmt.Errorf("sent must be between 1 and 100000")
	}
	if report.Received < 0 || report.Received > report.Sent {
		return fmt.Errorf("received must be between 0 and sent")
	}
	if report.LossPercent < 0 || report.LossPercent > 100 {
		return fmt.Errorf("lossPercent must be between 0 and 100")
	}
	expectedLoss := float64(report.Sent-report.Received) * 100 / float64(report.Sent)
	if math.Abs(report.LossPercent-expectedLoss) > 0.1 {
		return fmt.Errorf("lossPercent does not match sent and received counts")
	}
	for name, value := range map[string]float64{
		"rttMinMs":    report.RTTMin,
		"rttMedianMs": report.RTTMedian,
		"rttP90Ms":    report.RTTP90,
		"jitterMs":    report.JitterMs,
	} {
		if value < 0 || value > 60_000 {
			return fmt.Errorf("%s must be between 0 and 60000", name)
		}
	}
	if report.Received == 0 {
		if report.RTTMin != 0 || report.RTTMedian != 0 || report.RTTP90 != 0 || report.JitterMs != 0 {
			return fmt.Errorf("RTT and jitter must be zero when no acknowledgements were received")
		}
		return nil
	}
	if report.RTTMin > report.RTTMedian || report.RTTMedian > report.RTTP90 {
		return fmt.Errorf("RTT values must satisfy min <= median <= p90")
	}
	if math.Abs(report.JitterMs-(report.RTTP90-report.RTTMedian)) > 0.1 {
		return fmt.Errorf("jitterMs must equal rttP90Ms minus rttMedianMs")
	}
	return nil
}

// handlePacketTestReport handles POST /api/packet-test/report.
// This endpoint receives packet loss test results from the client.
func (s *Server) handlePacketTestReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PacketTestReportRequest
	if !s.decodeControlJSON(w, r, s.cfg.MaxReportBodyBytes, &req) {
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

	if err := validatePacketTestReport(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	clientIP := s.clientIP(r)
	snapshot, ok := s.webrtcManager.CompletePacketLossSession(req.TestID, clientIP)
	if !ok {
		http.Error(w, "packet test session not found", http.StatusNotFound)
		return
	}

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

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.metrics.internalFailures.Add(1)
		log.Printf("Packet test report response write error: testId=%s error=%v", req.TestID, err)
	}
}
