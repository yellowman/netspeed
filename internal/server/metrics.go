package server

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/yellowman/netspeed/internal/telemetry"
)

type serviceMetrics struct {
	httpRequests               atomic.Uint64
	httpActive                 atomic.Int64
	authRejected               atomic.Uint64
	activeTransfers            atomic.Int64
	transferRejectedGlobal     atomic.Uint64
	transferRejectedClient     atomic.Uint64
	bandwidthQuotaRejected     atomic.Uint64
	downloadBytes              atomic.Uint64
	uploadBytes                atomic.Uint64
	webrtcOffers               atomic.Uint64
	webrtcOfferRateRejected    atomic.Uint64
	webrtcCapacityRejected     atomic.Uint64
	turnCredentialsIssued      atomic.Uint64
	turnCredentialRateRejected atomic.Uint64
	controlBodiesRejected      atomic.Uint64
	internalFailures           atomic.Uint64
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	writeMetric := func(name, help string, value any) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, value)
	}
	writeCounter := func(name, help string, value uint64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
	}

	writeCounter("netspeed_http_requests_total", "HTTP requests handled.", s.metrics.httpRequests.Load())
	writeMetric("netspeed_http_active", "HTTP requests currently executing.", s.metrics.httpActive.Load())
	writeCounter("netspeed_auth_rejected_total", "Requests rejected by shared-token authentication.", s.metrics.authRejected.Load())
	writeMetric("netspeed_transfers_active", "Measurement transfers currently active.", s.metrics.activeTransfers.Load())
	writeCounter("netspeed_transfer_rejected_global_total", "Transfers rejected by the global concurrency ceiling.", s.metrics.transferRejectedGlobal.Load())
	writeCounter("netspeed_transfer_rejected_client_total", "Transfers rejected by a per-client concurrency ceiling.", s.metrics.transferRejectedClient.Load())
	writeCounter("netspeed_bandwidth_quota_rejected_total", "Transfers rejected by a per-client byte quota.", s.metrics.bandwidthQuotaRejected.Load())
	writeCounter("netspeed_download_bytes_total", "Response payload bytes written by /__down.", s.metrics.downloadBytes.Load())
	writeCounter("netspeed_upload_bytes_total", "Request payload bytes consumed by /__up.", s.metrics.uploadBytes.Load())
	writeCounter("netspeed_webrtc_offers_total", "WebRTC offers admitted to signaling.", s.metrics.webrtcOffers.Load())
	writeCounter("netspeed_webrtc_offer_rate_rejected_total", "WebRTC offers rejected by per-client rate limiting.", s.metrics.webrtcOfferRateRejected.Load())
	writeCounter("netspeed_webrtc_capacity_rejected_total", "WebRTC offers rejected by active-session ceilings.", s.metrics.webrtcCapacityRejected.Load())
	activeSessions := 0
	if s.webrtcManager != nil {
		activeSessions = s.webrtcManager.SessionCount()
	}
	writeMetric("netspeed_webrtc_sessions_active", "WebRTC sessions currently owned by the manager.", activeSessions)
	writeMetric("netspeed_transfer_limit_global", "Configured global active-transfer ceiling.", s.cfg.MaxConcurrentTransfers)
	writeMetric("netspeed_transfer_limit_per_client", "Configured per-client active-transfer ceiling.", s.cfg.MaxConcurrentTransfersPerClient)
	writeMetric("netspeed_webrtc_session_limit_global", "Configured global WebRTC-session ceiling.", s.cfg.MaxWebRTCSessions)
	writeMetric("netspeed_webrtc_session_limit_per_client", "Configured per-client WebRTC-session ceiling.", s.cfg.MaxWebRTCSessionsPerClient)
	writeCounter("netspeed_turn_credentials_issued_total", "TURN credential responses issued.", s.metrics.turnCredentialsIssued.Load())
	writeCounter("netspeed_turn_credential_rate_rejected_total", "TURN credential requests rejected by rate limiting.", s.metrics.turnCredentialRateRejected.Load())
	writeCounter("netspeed_control_body_rejected_total", "Control-plane request bodies rejected as invalid or oversized.", s.metrics.controlBodiesRejected.Load())
	writeCounter("netspeed_internal_failures_total", "Internal handler or response failures observed.", s.metrics.internalFailures.Load())

	var relay telemetry.RelayStats
	if s.relayStats != nil {
		relay = s.relayStats.RelayStats()
	}
	writeCounter("netspeed_turn_udp_read_bytes_total", "Bytes accepted from the embedded TURN UDP socket.", relay.BytesRead)
	writeCounter("netspeed_turn_udp_written_bytes_total", "Bytes written by the embedded TURN UDP socket.", relay.BytesWritten)
	writeCounter("netspeed_turn_udp_read_packets_total", "Packets accepted from the embedded TURN UDP socket.", relay.PacketsRead)
	writeCounter("netspeed_turn_udp_written_packets_total", "Packets written by the embedded TURN UDP socket.", relay.PacketsWritten)
	writeCounter("netspeed_turn_udp_dropped_read_bytes_total", "Inbound embedded TURN bytes dropped by its rate ceiling.", relay.DroppedReadBytes)
	writeCounter("netspeed_turn_udp_rejected_write_bytes_total", "Outbound embedded TURN bytes rejected by its rate ceiling.", relay.RejectedWriteBytes)
}

// SetRelayStatsProvider attaches embedded TURN accounting after both services
// have been constructed. It must be called before Run.
func (s *Server) SetRelayStatsProvider(provider telemetry.RelayStatsProvider) {
	s.relayStats = provider
}
