package cloudflarecompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cloudflareLatencyAttempts   = 4
	cloudflareServerTimingMinMS = 0.01
)

type cloudflareLatencyAttempt struct {
	milliseconds float64
	reused       bool
	adjusted     bool
	protocol     string
}

type cloudflareLatencySession struct {
	o                           options
	client                      *http.Client
	transport                   *http.Transport
	warmupRequests              int
	discardedColdAttempts       int
	serverTimingAdjustedSamples int
	protocols                   map[string]struct{}
}

type cloudflareLatencyTiming struct {
	gotConnection bool
	reused        bool
	wroteRequest  time.Time
	firstByte     time.Time
}

func newCloudflareLatencySession(o options) *cloudflareLatencySession {
	transport := newHTTPTransport(o)
	transport.MaxIdleConns = 1
	transport.MaxIdleConnsPerHost = 1
	transport.MaxConnsPerHost = 1
	return &cloudflareLatencySession{
		o:         o,
		transport: transport,
		client:    &http.Client{Transport: transport, Timeout: o.Timeout},
		protocols: make(map[string]struct{}),
	}
}

func (session *cloudflareLatencySession) Close() {
	session.transport.CloseIdleConnections()
}

func (session *cloudflareLatencySession) Prime(ctx context.Context, during string) error {
	session.warmupRequests++
	_, err := session.attempt(ctx, during, -1, 0)
	if err != nil {
		return fmt.Errorf("prime warm Cloudflare latency connection: %w", err)
	}
	return nil
}

func (session *cloudflareLatencySession) Probe(ctx context.Context, during string, sequence int) (float64, error) {
	var last cloudflareLatencyAttempt
	for attempt := 0; attempt < cloudflareLatencyAttempts; attempt++ {
		measurement, err := session.attempt(ctx, during, sequence, attempt)
		if err != nil {
			return 0, err
		}
		last = measurement
		if !measurement.reused {
			session.discardedColdAttempts++
			session.warmupRequests++
			continue
		}
		if measurement.adjusted {
			session.serverTimingAdjustedSamples++
		}
		if measurement.protocol != "" {
			session.protocols[measurement.protocol] = struct{}{}
		}
		return measurement.milliseconds, nil
	}
	return 0, fmt.Errorf("Cloudflare latency connection was not reused after %d attempts (last protocol %s)", cloudflareLatencyAttempts, last.protocol)
}

func (session *cloudflareLatencySession) attempt(ctx context.Context, during string, sequence, attempt int) (cloudflareLatencyAttempt, error) {
	query := url.Values{
		"bytes":   {"0"},
		"during":  {during},
		"id":      {strconv.FormatInt(time.Now().UnixNano(), 10)},
		"seq":     {strconv.Itoa(sequence)},
		"attempt": {strconv.Itoa(attempt)},
	}

	var timing cloudflareLatencyTiming
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			timing.gotConnection = true
			timing.reused = info.Reused
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			timing.wroteRequest = time.Now()
		},
		GotFirstResponseByte: func() {
			timing.firstByte = time.Now()
		},
	}
	traceContext := httptrace.WithClientTrace(ctx, trace)
	response, err := request(traceContext, session.client, session.o, http.MethodGet, "/__down", query, nil, -1)
	if err != nil {
		return cloudflareLatencyAttempt{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return cloudflareLatencyAttempt{}, fmt.Errorf("latency HTTP %d", response.StatusCode)
	}
	if err := verifyCloudflareIdentityResponse(response); err != nil {
		return cloudflareLatencyAttempt{}, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1))
	if err != nil {
		return cloudflareLatencyAttempt{}, err
	}
	if len(body) != 0 {
		return cloudflareLatencyAttempt{}, fmt.Errorf("Cloudflare zero-byte latency probe returned %d body bytes", len(body))
	}
	if !timing.gotConnection || timing.wroteRequest.IsZero() || timing.firstByte.IsZero() {
		return cloudflareLatencyAttempt{}, errors.New("Cloudflare latency httptrace did not capture connection, request-write, and first-byte events")
	}
	rtt := timing.firstByte.Sub(timing.wroteRequest)
	if rtt <= 0 {
		return cloudflareLatencyAttempt{}, fmt.Errorf("non-positive Cloudflare latency duration %s", rtt)
	}
	milliseconds := float64(rtt) / float64(time.Millisecond)
	adjusted := false
	if serverDuration := prioritizedServerDurationMS(strings.Join(response.Header.Values("Server-Timing"), ",")); serverDuration > 0 && serverDuration < milliseconds {
		milliseconds -= serverDuration
		adjusted = true
	}
	if milliseconds <= 0 {
		return cloudflareLatencyAttempt{}, errors.New("non-positive Cloudflare latency after server-duration adjustment")
	}
	return cloudflareLatencyAttempt{
		milliseconds: milliseconds,
		reused:       timing.reused,
		adjusted:     adjusted,
		protocol:     response.Proto,
	}, nil
}

func (session *cloudflareLatencySession) Summarize(values []float64, minimum int, errorText string) latencySummary {
	summary := summarizeLatency(values, minimum, errorText)
	summary.ConnectionReused = len(values) > 0
	summary.WarmSamples = len(values)
	summary.WarmupRequests = session.warmupRequests
	summary.DiscardedColdAttempts = session.discardedColdAttempts
	summary.ServerTimingAdjustedSamples = session.serverTimingAdjustedSamples
	summary.ProbeTransport = "http"
	summary.ProbeMethod = http.MethodGet
	summary.ProbePath = "/__down"
	for protocol := range session.protocols {
		summary.HTTPProtocols = append(summary.HTTPProtocols, protocol)
	}
	sort.Strings(summary.HTTPProtocols)
	return summary
}

func prioritizedServerDurationMS(header string) float64 {
	if strings.TrimSpace(header) == "" {
		return 0
	}
	type timingMetric struct {
		name     string
		duration float64
	}
	metrics := make([]timingMetric, 0, 4)
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ";")
		if len(parts) == 0 {
			continue
		}
		metric := timingMetric{name: strings.ToLower(strings.TrimSpace(parts[0]))}
		for _, parameter := range parts[1:] {
			name, raw, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "dur") {
				continue
			}
			value, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(raw), `"`), 64)
			if err == nil && value > cloudflareServerTimingMinMS {
				metric.duration = value
				break
			}
		}
		if metric.duration > 0 {
			metrics = append(metrics, metric)
		}
	}

	// Cloudflare's published parser prefers the cfReqDur family, which is an
	// end-to-end server-duration value. When that value is absent, cfSpeed*
	// entries are component durations and are summed. The generic app metric is
	// only a final fallback so a daemon's app/cfSpeedApp alias pair is not counted
	// twice.
	for _, metric := range metrics {
		switch metric.name {
		case "cfreqdur", "cfrequestdur", "cfreqduration", "cfrequestduration":
			return metric.duration
		}
	}
	cfspeedTotal := 0.0
	for _, metric := range metrics {
		if strings.HasPrefix(metric.name, "cfspeed") {
			cfspeedTotal += metric.duration
		}
	}
	if cfspeedTotal > 0 {
		return cfspeedTotal
	}
	for _, metric := range metrics {
		if metric.name == "app" {
			return metric.duration
		}
	}
	return 0
}
