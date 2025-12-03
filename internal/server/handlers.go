package server

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/yellowman/netspeed/internal/meta"
)

// handleMeta handles GET /meta - returns client metadata.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientMeta := s.metaProvider.MetaFor(r)

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

	// Parse bytes parameter
	bytesStr := r.URL.Query().Get("bytes")
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

	// Get client meta for headers
	clientMeta := s.metaProvider.MetaFor(r)

	// Set headers
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(nBytes, 10))
	s.setMetaHeaders(w, clientMeta, start)

	// Set Server-Timing header before body starts (measures server-side latency)
	// Note: For streaming responses, this reflects setup time, not total transfer time
	s.setServerTiming(w, start)

	// If bytes == 0, just return (latency-only test)
	if nBytes == 0 {
		w.WriteHeader(http.StatusOK)
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
			// Client probably disconnected; abort
			return
		}
		remaining -= int64(n)
	}
}

// handleUp handles POST /__up - upload sink endpoint.
func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	// Read and discard body safely with limit
	n, err := io.Copy(io.Discard, io.LimitReader(r.Body, s.cfg.MaxBytes))
	if err != nil && err != io.EOF {
		log.Printf("Upload read error: %v", err)
	}

	// Log upload details as per spec
	duration := time.Since(start)
	measId := r.URL.Query().Get("measId")
	clientIP := meta.ClientIPFromRequest(r, s.cfg.TrustProxyHeaders)
	log.Printf("Upload: client=%s measId=%s bytes=%d duration=%s", clientIP, measId, n, duration)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.setServerTiming(w, start)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
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

	// Check if TURN is configured
	if s.cfg.TurnSecret == "" || len(s.cfg.TurnServers) == 0 {
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
		Servers:    s.cfg.TurnServers,
		Realm:      s.cfg.TurnRealm,
	}

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

	// Log the report
	clientIP := meta.ClientIPFromRequest(r, s.cfg.TrustProxyHeaders)
	log.Printf("Packet test report: testId=%s client=%s sent=%d received=%d loss=%.2f%% rtt=[%.2f/%.2f/%.2f]ms jitter=%.2fms",
		req.TestID, clientIP, req.Sent, req.Received, req.LossPercent,
		req.RTTMin, req.RTTMedian, req.RTTP90, req.JitterMs)

	// Clean up the session if it exists
	if s.webrtcManager != nil && req.TestID != "" {
		s.webrtcManager.CloseSession(req.TestID)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}
