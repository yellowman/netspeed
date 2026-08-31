package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yellowman/netspeed/internal/measurementhttp"
	"github.com/yellowman/netspeed/internal/websocketping"
)

// websocketLatencyProbe owns the one persistent application-level ping
// connection used by unloaded and loaded latency tests. Any upgrade or protocol
// failure disables it for the remainder of the run so callers automatically use
// the already-negotiated warm HTTP fallback instead of repeatedly stalling.
type websocketLatencyProbe struct {
	mutex          sync.Mutex
	serverURL      string
	endpointPath   string
	accessToken    string
	timeout        time.Duration
	connection     *websocketping.Client
	disabled       bool
	fallbackReason string
	connections    int
	warmups        int
	pings          int
}

func newWebSocketLatencyProbe(cfg Config, selection measurementhttp.Selection) *websocketLatencyProbe {
	if selection.WebSocketPingPath == "" || selection.WebSocketPingProtocol != measurementhttp.WebSocketPingSubprotocol ||
		selection.WebSocketPingPayloadBytes != measurementhttp.WebSocketPingPayloadBytes {
		return nil
	}
	timeout := 10 * time.Second
	if cfg.Timeout > 0 && cfg.Timeout < timeout {
		timeout = cfg.Timeout
	}
	return &websocketLatencyProbe{
		serverURL:    cfg.ServerURL,
		endpointPath: selection.WebSocketPingPath,
		accessToken:  cfg.AccessToken,
		timeout:      timeout,
	}
}

// measure returns (probe, true, "") when WebSocket supplied the sample. A false
// result includes the stable reason that should be attached to the HTTP fallback
// sample. The mutex intentionally serializes messages on one socket; waiting for
// it occurs before websocketping.Client starts the RTT clock.
func (probe *websocketLatencyProbe) measure(ctx context.Context, sequence int) (latencyProbeMeasurement, bool, string) {
	if probe == nil {
		return latencyProbeMeasurement{}, false, ""
	}
	probe.mutex.Lock()
	defer probe.mutex.Unlock()
	if probe.disabled {
		return latencyProbeMeasurement{}, false, probe.fallbackReason
	}
	if probe.connection == nil {
		connection, err := websocketping.Dial(ctx, probe.serverURL, probe.endpointPath, probe.accessToken, UserAgent, probe.timeout)
		if err != nil {
			return latencyProbeMeasurement{}, false, probe.disableLocked(fmt.Errorf("upgrade failed: %w", err))
		}
		probe.connection = connection
		probe.connections++

		// Verify and warm the just-established application path before reporting
		// any latency sample. TCP/TLS/Upgrade and this warmup are all excluded.
		warmup, err := websocketping.NewPayload(^uint32(0))
		if err != nil {
			return latencyProbeMeasurement{}, false, probe.disableLocked(err)
		}
		if _, err := probe.connection.Ping(ctx, warmup); err != nil {
			return latencyProbeMeasurement{}, false, probe.disableLocked(fmt.Errorf("warmup failed: %w", err))
		}
		probe.warmups++
	}

	payload, err := websocketping.NewPayload(uint32(sequence))
	if err != nil {
		return latencyProbeMeasurement{}, false, probe.disableLocked(err)
	}
	rtt, err := probe.connection.Ping(ctx, payload)
	if err != nil {
		return latencyProbeMeasurement{}, false, probe.disableLocked(fmt.Errorf("message failed: %w", err))
	}
	probe.pings++
	return latencyProbeMeasurement{
		RTT:               rtt,
		ConnectionReused:  true,
		Transport:         "websocket",
		TimingSource:      "websocket-message",
		Method:            "MESSAGE",
		Path:              probe.endpointPath,
		WebSocketProtocol: measurementhttp.WebSocketPingSubprotocol,
	}, true, ""
}

func (probe *websocketLatencyProbe) disableLocked(err error) string {
	if probe.connection != nil {
		_ = probe.connection.Close()
		probe.connection = nil
	}
	probe.disabled = true
	probe.fallbackReason = "WebSocket latency unavailable"
	if err != nil {
		probe.fallbackReason += ": " + err.Error()
	}
	return probe.fallbackReason
}

func (probe *websocketLatencyProbe) close() {
	if probe == nil {
		return
	}
	probe.mutex.Lock()
	defer probe.mutex.Unlock()
	if probe.connection != nil {
		_ = probe.connection.Close()
		probe.connection = nil
	}
}
