package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/limits"
	"github.com/yellowman/netspeed/internal/locations"
)

func TestAuthenticationMiddlewareProtectsServiceButNotHealth(t *testing.T) {
	server := measurementTestServer(1024)
	server.cfg.AccessToken = "0123456789abcdef"
	handler := server.authenticationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/meta", "/__ping"} {
		unauthorized := httptest.NewRecorder()
		handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s status=%d; want 401", path, unauthorized.Code)
		}
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/meta", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer 0123456789abcdef")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized status=%d; want 204", authorized.Code)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusNoContent {
		t.Fatalf("health status=%d; want 204", health.Code)
	}
}

func TestAuthenticatedLocationsCannotBeStoredBySharedCaches(t *testing.T) {
	server := measurementTestServer(1024)
	server.cfg.AccessToken = "0123456789abcdef"
	server.locations = locations.NewMemoryStore(nil)

	handler := server.authenticationMiddleware(http.HandlerFunc(server.handleLocations))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/locations", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d; want 401", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/locations", nil)
	request.Header.Set("Authorization", "Bearer "+server.cfg.AccessToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized status=%d; want 200", recorder.Code)
	}
	cacheControl := recorder.Header().Get("Cache-Control")
	if cacheControl != "private, no-store" {
		t.Fatalf("Cache-Control=%q; want private, no-store", cacheControl)
	}
	if strings.Contains(strings.ToLower(cacheControl), "public") {
		t.Fatalf("protected locations response is publicly cacheable: %q", cacheControl)
	}
	if got := recorder.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma=%q; want no-cache", got)
	}
	if got := strings.Join(recorder.Header().Values("Vary"), ","); !strings.Contains(got, "Authorization") {
		t.Fatalf("Vary=%q; want Authorization", got)
	}
}

func TestBeginTransferEnforcesPerClientAndGlobalCeilings(t *testing.T) {
	server := measurementTestServer(1024)
	server.transferLimiter = limits.NewTransferLimiter(2, 1)

	requestA := httptest.NewRequest(http.MethodGet, "/__down?bytes=1", nil)
	requestA.RemoteAddr = "198.51.100.1:1000"
	releaseA, ok := server.beginTransfer(httptest.NewRecorder(), requestA)
	if !ok {
		t.Fatal("first transfer rejected")
	}
	defer releaseA()

	clientReject := httptest.NewRecorder()
	if _, ok := server.beginTransfer(clientReject, requestA); ok || clientReject.Code != http.StatusTooManyRequests {
		t.Fatalf("client rejection ok=%v status=%d", ok, clientReject.Code)
	}
	if clientReject.Header().Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("client rejection Cache-Control=%q", clientReject.Header().Get("Cache-Control"))
	}

	requestB := httptest.NewRequest(http.MethodGet, "/__down?bytes=1", nil)
	requestB.RemoteAddr = "198.51.100.2:1000"
	releaseB, ok := server.beginTransfer(httptest.NewRecorder(), requestB)
	if !ok {
		t.Fatal("second client transfer rejected")
	}
	defer releaseB()

	requestC := httptest.NewRequest(http.MethodGet, "/__down?bytes=1", nil)
	requestC.RemoteAddr = "198.51.100.3:1000"
	globalReject := httptest.NewRecorder()
	if _, ok := server.beginTransfer(globalReject, requestC); ok || globalReject.Code != http.StatusServiceUnavailable {
		t.Fatalf("global rejection ok=%v status=%d", ok, globalReject.Code)
	}
}

func TestHandleDownRejectsBandwidthQuotaBeforeWritingPayload(t *testing.T) {
	server := measurementTestServer(1024)
	server.cfg.ClientBandwidthQuotaBytes = 4
	server.cfg.ClientBandwidthQuotaWindow = time.Hour
	server.bandwidthQuota = limits.NewByteQuota(4, time.Hour)

	request := httptest.NewRequest(http.MethodGet, "/__down?bytes=5", nil)
	recorder := httptest.NewRecorder()
	server.handleDown(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d; want 429", recorder.Code)
	}
	if recorder.Body.Len() == 5 {
		t.Fatal("quota rejection unexpectedly wrote payload")
	}
}

func TestHandleUnknownLengthUploadStopsAtQuota(t *testing.T) {
	server := measurementTestServer(1024)
	server.cfg.ClientBandwidthQuotaBytes = 4
	server.cfg.ClientBandwidthQuotaWindow = time.Hour
	server.bandwidthQuota = limits.NewByteQuota(4, time.Hour)

	request := httptest.NewRequest(http.MethodPost, "/__up", bytes.NewReader([]byte("12345")))
	request.ContentLength = -1
	recorder := httptest.NewRecorder()
	server.handleUp(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d; want 429; body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestDecodeControlJSONRejectsOversizeAndTrailingValues(t *testing.T) {
	server := measurementTestServer(1024)

	oversizeRequest := httptest.NewRequest(http.MethodPost, "/api/packet-test/report", strings.NewReader(`{"value":"too large"}`))
	oversizeRequest.Header.Set("Content-Type", "application/json")
	oversize := httptest.NewRecorder()
	var value map[string]any
	if server.decodeControlJSON(oversize, oversizeRequest, 4, &value) {
		t.Fatal("oversized body accepted")
	}
	if oversize.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d; want 413", oversize.Code)
	}

	trailingRequest := httptest.NewRequest(http.MethodPost, "/api/packet-test/report", strings.NewReader(`{} {}`))
	trailingRequest.Header.Set("Content-Type", "application/json")
	trailing := httptest.NewRecorder()
	if server.decodeControlJSON(trailing, trailingRequest, 64, &value) {
		t.Fatal("trailing JSON accepted")
	}
	if trailing.Code != http.StatusBadRequest {
		t.Fatalf("trailing status=%d; want 400", trailing.Code)
	}
}

func TestAuthenticationMiddlewareUsesIndependentMetricsToken(t *testing.T) {
	server := measurementTestServer(1024)
	server.cfg.AccessToken = "service-token-012345"
	server.cfg.MetricsToken = "metrics-token-012345"
	handler := server.authenticationMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	serviceCredential := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	serviceCredential.Header.Set("Authorization", "Bearer "+server.cfg.AccessToken)
	serviceRecorder := httptest.NewRecorder()
	handler.ServeHTTP(serviceRecorder, serviceCredential)
	if serviceRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("service token metrics status=%d; want 401", serviceRecorder.Code)
	}

	metricsCredential := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsCredential.Header.Set("Authorization", "Bearer "+server.cfg.MetricsToken)
	metricsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(metricsRecorder, metricsCredential)
	if metricsRecorder.Code != http.StatusNoContent {
		t.Fatalf("metrics token status=%d; want 204", metricsRecorder.Code)
	}
}

func TestDecodeControlJSONRequiresJSONContentType(t *testing.T) {
	server := measurementTestServer(1024)
	request := httptest.NewRequest(http.MethodPost, "/api/packet-test/report", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	var value map[string]any
	if server.decodeControlJSON(recorder, request, 64, &value) {
		t.Fatal("control body without Content-Type accepted")
	}
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d; want 415", recorder.Code)
	}
}

func TestHandleMetricsWorksWithoutWebRTCOrTURNProviders(t *testing.T) {
	server := measurementTestServer(1024)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	server.handleMetrics(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, metric := range []string{
		"netspeed_webrtc_sessions_active 0",
		"netspeed_transfer_limit_global",
		"netspeed_turn_udp_read_packets_total 0",
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("metrics body missing %q:\n%s", metric, body)
		}
	}
}

func TestValidatePacketTestReportRejectsImpossibleValues(t *testing.T) {
	valid := PacketTestReportRequest{TestID: "id", Sent: 1000, Received: 990, LossPercent: 1, RTTMin: 1, RTTMedian: 2, RTTP90: 3, JitterMs: 1}
	if err := validatePacketTestReport(valid); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	invalid := valid
	invalid.Received = invalid.Sent + 1
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("received > sent accepted")
	}
	invalid = valid
	invalid.LossPercent = 101
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("loss over 100 accepted")
	}
	invalid = valid
	invalid.RTTP90 = 60_001
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("unbounded RTT accepted")
	}
	invalid = valid
	invalid.Sent = 0
	invalid.Received = 0
	invalid.LossPercent = 0
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("zero-probe report accepted")
	}
	invalid = valid
	invalid.LossPercent = 2
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("loss inconsistent with sent/received accepted")
	}
	invalid = valid
	invalid.RTTMedian = 4
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("unordered RTT values accepted")
	}
	invalid = valid
	invalid.JitterMs = 2
	if err := validatePacketTestReport(invalid); err == nil {
		t.Fatal("jitter inconsistent with p90-minus-median accepted")
	}
}

func TestTurnConfigurationEndpointSupportsPublicSTUNWithoutSecret(t *testing.T) {
	server := measurementTestServer(1024)
	server.cfg.TurnServers = []string{"stun:stun.example.test:3478"}
	server.cfg.TurnSecret = ""

	request := httptest.NewRequest(http.MethodGet, "/api/turn/credentials", nil)
	request.RemoteAddr = "198.51.100.20:1234"
	recorder := httptest.NewRecorder()
	server.handleTurnCredentials(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	var response TurnCredentialsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Servers) != 1 || response.Servers[0] != server.cfg.TurnServers[0] {
		t.Fatalf("servers=%v; want configured STUN server", response.Servers)
	}
	if response.Username != "" || response.Credential != "" || response.TTLSec != 0 {
		t.Fatalf("public STUN response unexpectedly contains credentials: %+v", response)
	}
}
