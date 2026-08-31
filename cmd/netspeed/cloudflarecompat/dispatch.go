package cloudflarecompat

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	providerAuto       = "auto"
	providerNetspeed   = "netspeed"
	providerCloudflare = "cloudflare"
)

type options struct {
	Provider           string
	ProviderExplicit   bool
	Server             string
	Token              string
	JSON               bool
	Quiet              bool
	CSV                bool
	Quick              bool
	DownloadOnly       bool
	UploadOnly         bool
	SkipPacketLoss     bool
	Insecure           bool
	Timeout            time.Duration
	TurnCredentialsURL string
	TurnURL            string
	TurnUsername       string
	TurnCredential     string
	TransportControls  bool
}

type probeResult struct {
	Netspeed       bool
	Incompatible   bool
	Cloudflare     bool
	Detail         string
	MetaStatusCode int
}

type sampleSummary struct {
	Available bool      `json:"available"`
	BPS       *float64  `json:"bps,omitempty"`
	Mbps      *float64  `json:"mbps,omitempty"`
	Samples   []float64 `json:"samplesMbps,omitempty"`
	Evidence  string    `json:"evidence,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type latencySummary struct {
	Available bool      `json:"available"`
	MedianMS  *float64  `json:"medianMs,omitempty"`
	P90MS     *float64  `json:"p90Ms,omitempty"`
	JitterMS  *float64  `json:"jitterMs,omitempty"`
	SamplesMS []float64 `json:"samplesMs,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type packetSummary struct {
	Available                         bool     `json:"available"`
	Transport                         string   `json:"transport"`
	Topology                          string   `json:"topology"`
	Protocol                          string   `json:"protocol"`
	Sent                              int      `json:"sent,omitempty"`
	Received                          int      `json:"received,omitempty"`
	Lost                              int      `json:"lost,omitempty"`
	TransactionLossPercent            *float64 `json:"transactionLossPercent"`
	ForwardLossPercent                *float64 `json:"forwardLossPercent"`
	ReverseAcknowledgementLossPercent *float64 `json:"reverseAcknowledgementLossPercent"`
	Reason                            string   `json:"reason,omitempty"`
}

type result struct {
	Provider            string         `json:"provider"`
	MeasurementContract string         `json:"measurementContract"`
	UploadEvidence      string         `json:"uploadEvidence"`
	PacketTopology      string         `json:"packetTopology"`
	Server              string         `json:"server"`
	StartedAt           time.Time      `json:"startedAt"`
	FinishedAt          time.Time      `json:"finishedAt"`
	Latency             latencySummary `json:"latency"`
	Download            sampleSummary  `json:"download"`
	Upload              sampleSummary  `json:"upload"`
	DownloadLoaded      latencySummary `json:"downloadLoadedLatency"`
	UploadLoaded        latencySummary `json:"uploadLoadedLatency"`
	PacketLoss          packetSummary  `json:"packetLoss"`
}

// Dispatch selects the Cloudflare-compatible engine when requested or when an
// auto probe positively fingerprints Cloudflare. It returns false to let the
// existing strict Netspeed client continue.
func Dispatch(args []string) (bool, int) {
	opts, stripped, err := parseOptions(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "netspeed:", err)
		return true, 2
	}
	os.Args = append([]string{os.Args[0]}, stripped...)
	if hasHelpOrVersion(args) {
		return false, 0
	}

	switch opts.Provider {
	case providerNetspeed:
		setIdentity(providerNetspeed, "netspeed-verified-v2", "server-peer")
		return false, 0
	case providerCloudflare:
		if opts.TransportControls {
			fmt.Fprintln(os.Stderr, "netspeed: download transport discriminator flags currently require --provider netspeed")
			return true, 2
		}
		return true, runCloudflare(opts)
	case providerAuto:
		p, err := probeProvider(opts)
		if err != nil {
			// Do not downgrade an unreachable or unknown endpoint. Let the
			// strict client produce its normal diagnostic.
			setIdentity(providerNetspeed, "netspeed-verified-v2", "server-peer")
			return false, 0
		}
		if p.Netspeed || p.Incompatible {
			setIdentity(providerNetspeed, "netspeed-verified-v2", "server-peer")
			return false, 0
		}
		if p.Cloudflare {
			if opts.TransportControls {
				fmt.Fprintln(os.Stderr, "netspeed: download transport discriminator flags currently require a Netspeed server advertising measurementCapabilities")
				return true, 2
			}
			return true, runCloudflare(opts)
		}
		setIdentity(providerNetspeed, "netspeed-verified-v2", "server-peer")
		return false, 0
	default:
		fmt.Fprintf(os.Stderr, "netspeed: invalid provider %q (want auto, netspeed, or cloudflare)\n", opts.Provider)
		return true, 2
	}
}

// WriteUsage appends the provider and TURN options consumed before the ordinary
// flag package parses the strict Netspeed options.
func WriteUsage(w io.Writer) {
	fmt.Fprintln(w, "  --provider MODE             auto, netspeed, or cloudflare")
	fmt.Fprintln(w, "  --turn-credentials-url URL Cloudflare-compatible TURN credential endpoint")
	fmt.Fprintln(w, "  --turn-url URL             Direct TURN URL for relay-only loopback")
	fmt.Fprintln(w, "  --turn-username USER       Direct TURN username")
	fmt.Fprintln(w, "  --turn-credential PASS     Direct TURN credential")
	fmt.Fprintln(w, "  --insecure                 Disable TLS verification in Cloudflare mode")
}

func setIdentity(provider, contract, topology string) {
	_ = os.Setenv("NETSPEED_SELECTED_PROVIDER", provider)
	_ = os.Setenv("NETSPEED_MEASUREMENT_CONTRACT", contract)
	_ = os.Setenv("NETSPEED_PACKET_TOPOLOGY", topology)
}

func transportControlIsExplicit(name, value string) bool {
	value = strings.TrimSpace(value)
	if name == "--download-chunk-bytes" {
		parsed, err := strconv.Atoi(value)
		return err != nil || parsed != 0
	}
	return value != "" && !strings.EqualFold(value, "auto")
}

func hasHelpOrVersion(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-version" || a == "--version" {
			return true
		}
	}
	return false
}

func parseOptions(args []string) (options, []string, error) {
	o := options{Provider: providerAuto, Server: "http://localhost:8080", Timeout: 30 * time.Second}
	stripped := make([]string, 0, len(args))
	serverExplicit := false
	positionalServer := false
	take := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", name)
		}
		*i = *i + 1
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, hasEq := strings.Cut(a, "=")
		switch key {
		case "--provider":
			o.ProviderExplicit = true
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.Provider = strings.ToLower(strings.TrimSpace(val))
		case "--server", "-s":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.Server = val
			serverExplicit = true
			stripped = append(stripped, a)
			if !hasEq {
				stripped = append(stripped, val)
			}
		case "--token":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.Token = val
			stripped = append(stripped, a)
			if !hasEq {
				stripped = append(stripped, val)
			}
		case "--json", "-j":
			o.JSON = true
			stripped = append(stripped, a)
		case "--quiet":
			o.Quiet = true
			stripped = append(stripped, a)
		case "--csv", "-c":
			o.CSV = true
			stripped = append(stripped, a)
		case "--quick", "-q":
			o.Quick = true
			stripped = append(stripped, a)
		case "--download-only", "-d":
			o.DownloadOnly = true
			stripped = append(stripped, a)
		case "--upload-only", "-u":
			o.UploadOnly = true
			stripped = append(stripped, a)
		case "--no-packet-loss", "--skip-packet-loss":
			o.SkipPacketLoss = true
			// The strict client understands --no-packet-loss. Normalize the
			// compatibility alias before handing control back to it.
			stripped = append(stripped, "--no-packet-loss")
		case "--insecure", "-k":
			o.Insecure = true
			// The Cloudflare adapter consumes this option. The strict client
			// does not currently expose an insecure-TLS mode.
		case "--timeout", "-t":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			d, err := time.ParseDuration(val)
			if err != nil || d <= 0 {
				if err == nil {
					err = errors.New("duration must be positive")
				}
				return o, nil, fmt.Errorf("invalid timeout: %w", err)
			}
			o.Timeout = d
			stripped = append(stripped, a)
			if !hasEq {
				stripped = append(stripped, val)
			}
		case "--download-payload", "--download-framing", "--download-chunk-bytes", "--download-flush":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			if transportControlIsExplicit(key, val) {
				o.TransportControls = true
			}
			stripped = append(stripped, a)
			if !hasEq {
				stripped = append(stripped, val)
			}
		case "--turn-credentials-url":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.TurnCredentialsURL = val
		case "--turn-url":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.TurnURL = val
		case "--turn-username":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.TurnUsername = val
		case "--turn-credential", "--turn-password":
			if !hasEq {
				var err error
				val, err = take(&i, key)
				if err != nil {
					return o, nil, err
				}
			}
			o.TurnCredential = val
		default:
			if !strings.HasPrefix(a, "-") {
				if serverExplicit || positionalServer {
					return o, nil, fmt.Errorf("unexpected positional argument %q", a)
				}
				o.Server = a
				positionalServer = true
			}
			stripped = append(stripped, a)
		}
	}
	if v := os.Getenv("NETSPEED_PROVIDER"); !o.ProviderExplicit && v != "" {
		o.Provider = strings.ToLower(strings.TrimSpace(v))
	}
	if o.Token == "" {
		o.Token = os.Getenv("NETSPEED_TOKEN")
	}
	if o.TurnCredentialsURL == "" {
		o.TurnCredentialsURL = os.Getenv("NETSPEED_TURN_CREDENTIALS_URL")
	}
	if o.TurnURL == "" {
		o.TurnURL = os.Getenv("NETSPEED_TURN_URL")
	}
	if o.TurnUsername == "" {
		o.TurnUsername = os.Getenv("NETSPEED_TURN_USERNAME")
	}
	if o.TurnCredential == "" {
		o.TurnCredential = os.Getenv("NETSPEED_TURN_CREDENTIAL")
	}
	if o.DownloadOnly && o.UploadOnly {
		return o, nil, errors.New("--download-only and --upload-only are mutually exclusive")
	}
	outputModes := 0
	for _, enabled := range []bool{o.JSON, o.CSV, o.Quiet} {
		if enabled {
			outputModes++
		}
	}
	if outputModes > 1 {
		return o, nil, errors.New("choose only one of --json, --csv, or --quiet")
	}
	u, err := url.ParseRequestURI(o.Server)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		if err == nil {
			err = errors.New("URL must use http or https and include a host")
		}
		return o, nil, fmt.Errorf("invalid server URL: %w", err)
	}
	return o, stripped, nil
}

func newHTTPClient(o options) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.Insecure} // #nosec G402 -- explicit CLI option
	tr.DisableCompression = true
	return &http.Client{Transport: tr, Timeout: o.Timeout}
}

func endpoint(base, path string, q url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	ref := &url.URL{Path: strings.TrimPrefix(path, "/")}
	out := u.ResolveReference(ref)
	out.RawQuery = q.Encode()
	return out.String(), nil
}

func request(ctx context.Context, client *http.Client, o options, method, path string, q url.Values, body io.Reader, n int64) (*http.Response, error) {
	raw, err := endpoint(o.Server, path, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return nil, err
	}
	if n >= 0 {
		req.ContentLength = n
	}
	req.Header.Set("Cache-Control", "no-store, no-transform")
	req.Header.Set("Accept", "application/json, */*")
	if o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+o.Token)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	return client.Do(req)
}

func probeProvider(o options) (probeResult, error) {
	client := newHTTPClient(o)
	ctx, cancel := context.WithTimeout(context.Background(), minDuration(o.Timeout, 8*time.Second))
	defer cancel()
	raw, _ := endpoint(o.Server, "/meta", nil)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if o.Token != "" {
		req.Header.Set("Authorization", "Bearer "+o.Token)
	}
	resp, err := client.Do(req)
	if err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		resp.Body.Close()
		var obj map[string]any
		if json.Unmarshal(body, &obj) == nil && looksNetspeedMeta(obj) {
			compatible := numberField(obj, "measurementProtocolVersion", "measurementApiVersion") == 2
			return probeResult{Netspeed: compatible, Incompatible: !compatible, Detail: "Netspeed metadata", MetaStatusCode: resp.StatusCode}, nil
		}
	}
	q := url.Values{"bytes": {"0"}, "compat": {strconv.FormatInt(time.Now().UnixNano(), 10)}}
	resp, err = request(ctx, client, o, http.MethodGet, "/__down", q, nil, -1)
	if err != nil {
		return probeResult{}, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	host := ""
	if u, e := url.Parse(o.Server); e == nil {
		host = strings.ToLower(u.Hostname())
	}
	cf := host == "speed.cloudflare.com" || strings.HasSuffix(host, ".cloudflare.com") || strings.HasSuffix(host, ".cloudflare.net")
	cf = cf || resp.Header.Get("CF-Ray") != "" || strings.Contains(strings.ToLower(resp.Header.Get("Server")), "cloudflare") || strings.Contains(strings.ToLower(resp.Header.Get("Server-Timing")), "cf")
	return probeResult{Cloudflare: cf, Detail: "Cloudflare HTTP fingerprint"}, nil
}

func looksNetspeedMeta(obj map[string]any) bool {
	for _, k := range []string{"measurementProtocolVersion", "measurementApiVersion", "uploadReceiptVersion", "maxTransferBytes", "packetFrameVersion"} {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}
func numberField(obj map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := obj[k].(float64); ok {
			return int(v)
		}
	}
	return 0
}
func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}

func runCloudflare(o options) int {
	_netspeedProgress := nsBeginProgress("speed test")
	defer _netspeedProgress.Done("complete")
	setIdentity(providerCloudflare, "cloudflare-http-v1", "turn-loopback")
	r := result{Provider: providerCloudflare, MeasurementContract: "cloudflare-http-v1", UploadEvidence: "client-observed-complete-body", PacketTopology: "turn-loopback", Server: o.Server, StartedAt: time.Now()}
	client := newHTTPClient(o)
	ctx, cancel := context.WithTimeout(context.Background(), maxDuration(o.Timeout, 45*time.Second))
	defer cancel()

	r.Latency = measureIdleLatency(ctx, client, o)
	if !o.UploadOnly {
		r.Download, r.DownloadLoaded = measureDirection(ctx, client, o, false)
	}
	if !o.DownloadOnly {
		r.Upload, r.UploadLoaded = measureDirection(ctx, client, o, true)
	}
	if o.SkipPacketLoss {
		r.PacketLoss = unavailablePacket("skipped by request")
	} else {
		r.PacketLoss = measureTURNLoopback(ctx, client, o)
	}
	r.FinishedAt = time.Now()
	if err := renderResult(r, o); err != nil {
		fmt.Fprintln(os.Stderr, "netspeed:", err)
		return 1
	}
	if !r.Latency.Available || (!o.UploadOnly && !r.Download.Available) || (!o.DownloadOnly && !r.Upload.Available) {
		return 1
	}
	return 0
}
func maxDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if a > b {
		return a
	}
	return b
}

func measureIdleLatency(ctx context.Context, client *http.Client, o options) latencySummary {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	count := 10
	if o.Quick {
		count = 5
	}
	vals := make([]float64, 0, count)
	for i := 0; i < count; i++ {
		d, err := latencyProbe(ctx, client, o, "idle")
		if err == nil {
			vals = append(vals, d)
		}
	}
	return summarizeLatency(vals, 3, "insufficient valid latency probes")
}

func latencyProbe(ctx context.Context, client *http.Client, o options, during string) (float64, error) {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	q := url.Values{"bytes": {"0"}, "during": {during}, "id": {strconv.FormatInt(time.Now().UnixNano(), 10)}}
	start := time.Now()
	resp, err := request(ctx, client, o, http.MethodGet, "/__down", q, nil, -1)
	if err != nil {
		return 0, err
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if readErr != nil {
		return 0, readErr
	}
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("latency HTTP %d", resp.StatusCode)
	}
	ms := float64(time.Since(start)) / float64(time.Millisecond)
	if sd := serverDurationMS(resp.Header.Get("Server-Timing")); sd > 0 && sd < ms {
		ms -= sd
	}
	if ms <= 0 {
		return 0, errors.New("non-positive latency")
	}
	return ms, nil
}

func serverDurationMS(h string) float64 {
	var sum float64
	for _, entry := range strings.Split(h, ",") {
		lower := strings.ToLower(strings.TrimSpace(entry))
		if !(strings.HasPrefix(lower, "cfreq") || strings.HasPrefix(lower, "cfspeed") || strings.HasPrefix(lower, "app")) {
			continue
		}
		if i := strings.Index(lower, "dur="); i >= 0 {
			v := lower[i+4:]
			if j := strings.IndexAny(v, "; "); j >= 0 {
				v = v[:j]
			}
			if f, e := strconv.ParseFloat(v, 64); e == nil {
				sum += f
			}
		}
	}
	return sum
}

type zeroReader struct {
	remaining int64
	read      int64
}

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > z.remaining {
		n = z.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = '0'
	}
	z.remaining -= n
	z.read += n
	return int(n), nil
}

func transferOnce(ctx context.Context, client *http.Client, o options, upload bool, size int64) (int64, time.Duration, error) {
	q := url.Values{"bytes": {strconv.FormatInt(size, 10)}, "id": {strconv.FormatInt(time.Now().UnixNano(), 10)}}
	start := time.Now()
	if upload {
		zr := &zeroReader{remaining: size}
		resp, err := request(ctx, client, o, http.MethodPost, "/__up", q, zr, size)
		if err != nil {
			return 0, 0, err
		}
		_, rerr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if rerr != nil {
			return 0, 0, rerr
		}
		if resp.StatusCode/100 != 2 {
			return 0, 0, fmt.Errorf("upload HTTP %d", resp.StatusCode)
		}
		if zr.read != size {
			return 0, 0, fmt.Errorf("transport consumed %d of %d upload bytes", zr.read, size)
		}
		return size, time.Since(start), nil
	}
	resp, err := request(ctx, client, o, http.MethodGet, "/__down", q, nil, -1)
	if err != nil {
		return 0, 0, err
	}
	n, rerr := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if rerr != nil {
		return 0, 0, rerr
	}
	if resp.StatusCode/100 != 2 {
		return 0, 0, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	if n != size {
		return 0, 0, fmt.Errorf("download returned %d of %d bytes", n, size)
	}
	return n, time.Since(start), nil
}

func measureDirection(ctx context.Context, client *http.Client, o options, upload bool) (sampleSummary, latencySummary) {
	warmSize := int64(1 << 20)
	if upload {
		warmSize = 512 << 10
	}
	n, d, err := transferOnce(ctx, client, o, upload, warmSize)
	if err != nil {
		return sampleSummary{Available: false, Error: err.Error()}, latencySummary{Available: false, Error: err.Error()}
	}
	estimate := float64(n*8) / d.Seconds()
	concurrency := 4
	duration := 1800 * time.Millisecond
	probes := 8
	if o.Quick {
		concurrency = 2
		duration = 900 * time.Millisecond
		probes = 4
	}
	chunk := int64(estimate * duration.Seconds() / 8 / float64(concurrency))
	if chunk < 256<<10 {
		chunk = 256 << 10
	}
	if chunk > 32<<20 {
		chunk = 32 << 20
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := time.Now().Add(duration)
	var total atomic.Int64
	samplesCh := make(chan float64, 65536) /* continuously drained; large guard for compatibility fixtures */
	var wg sync.WaitGroup
	ready := make(chan struct{}, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			for time.Now().Before(deadline) {
				n, d, e := transferOnce(runCtx, client, o, upload, chunk)
				if e != nil {
					if runCtx.Err() != nil {
						return
					}
					continue
				}
				total.Add(n)
				samplesCh <- float64(n*8) / d.Seconds() / 1e6
			}
		}()
	}
	for i := 0; i < concurrency; i++ {
		<-ready
	}
	latVals := make([]float64, 0, probes)
	for i := 0; i < probes && time.Now().Before(deadline); i++ {
		if v, e := latencyProbe(runCtx, client, o, map[bool]string{true: "upload", false: "download"}[upload]); e == nil {
			latVals = append(latVals, v)
		}
		time.Sleep(40 * time.Millisecond)
	}
	if rem := time.Until(deadline); rem > 0 {
		select {
		case <-time.After(rem):
		case <-ctx.Done():
		}
	}
	cancel()
	wg.Wait()
	close(samplesCh)
	vals := make([]float64, 0, len(samplesCh))
	for v := range samplesCh {
		vals = append(vals, v)
	}
	if len(vals) == 0 || total.Load() == 0 {
		return sampleSummary{Available: false, Error: "no complete transfer samples"}, summarizeLatency(latVals, 3, "insufficient loaded probes")
	}
	p := percentile(vals, 0.90)
	bps := p * 1e6
	return sampleSummary{Available: true, BPS: &bps, Mbps: &p, Samples: vals, Evidence: map[bool]string{true: "client-observed-complete-body", false: "exact-response-byte-count"}[upload]}, summarizeLatency(latVals, 3, "insufficient loaded probes")
}

func summarizeLatency(vals []float64, min int, errText string) latencySummary {
	_netspeedProgress := nsBeginProgress("latency probes")
	defer _netspeedProgress.Done("complete")
	if len(vals) < min {
		return latencySummary{Available: false, SamplesMS: vals, Error: errText}
	}
	sort.Float64s(vals)
	med := percentile(vals, .5)
	p90 := percentile(vals, .9)
	jitter := p90 - med
	return latencySummary{Available: true, MedianMS: &med, P90MS: &p90, JitterMS: &jitter, SamplesMS: vals}
}
func percentile(in []float64, p float64) float64 {
	if len(in) == 0 {
		return 0
	}
	v := append([]float64(nil), in...)
	sort.Float64s(v)
	if len(v) == 1 {
		return v[0]
	}
	x := p * float64(len(v)-1)
	lo := int(math.Floor(x))
	hi := int(math.Ceil(x))
	if lo == hi {
		return v[lo]
	}
	return v[lo] + (v[hi]-v[lo])*(x-float64(lo))
}

func unavailablePacket(reason string) packetSummary {
	_netspeedProgress := nsBeginProgress("packet delivery test")
	defer _netspeedProgress.Done("complete")
	return packetSummary{Available: false, Transport: "webrtc-datachannel-turn-udp", Topology: "turn-loopback", Protocol: "cloudflare-loopback-v1", Reason: reason}
}

func renderResult(r result, o options) error {
	if o.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	if o.CSV {
		fmt.Println("provider,contract,server,latency_ms,download_mbps,upload_mbps,download_loaded_ms,upload_loaded_ms,packet_loss_percent,packet_topology")
		fmt.Printf("%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n", r.Provider, r.MeasurementContract, r.Server, pfloat(r.Latency.MedianMS), pfloat(r.Download.Mbps), pfloat(r.Upload.Mbps), pfloat(r.DownloadLoaded.P90MS), pfloat(r.UploadLoaded.P90MS), pfloat(r.PacketLoss.TransactionLossPercent), r.PacketTopology)
		return nil
	}
	if o.Quiet {
		fmt.Printf("%s %s %s %s %s\n", pfloat(r.Download.Mbps), pfloat(r.Upload.Mbps), pfloat(r.Latency.MedianMS), pfloat(r.PacketLoss.TransactionLossPercent), r.Provider)
		return nil
	}
	fmt.Printf("Provider:            %s\n", r.Provider)
	fmt.Printf("Measurement contract: %s\n", r.MeasurementContract)
	fmt.Printf("Packet topology:      %s\n", r.PacketTopology)
	fmt.Printf("Latency:              %s ms\n", pfloat(r.Latency.MedianMS))
	fmt.Printf("Download:             %s Mbps\n", pfloat(r.Download.Mbps))
	fmt.Printf("Upload:               %s Mbps (%s)\n", pfloat(r.Upload.Mbps), r.UploadEvidence)
	fmt.Printf("Loaded latency down:  %s ms p90\n", pfloat(r.DownloadLoaded.P90MS))
	fmt.Printf("Loaded latency up:    %s ms p90\n", pfloat(r.UploadLoaded.P90MS))
	fmt.Printf("Packet loss:          %s %%\n", pfloat(r.PacketLoss.TransactionLossPercent))
	if !r.PacketLoss.Available && r.PacketLoss.Reason != "" {
		fmt.Printf("Packet test:          unavailable (%s)\n", r.PacketLoss.Reason)
	}
	return nil
}
func pfloat(v *float64) string {
	if v == nil {
		return "N/A"
	}
	return strconv.FormatFloat(*v, 'f', 2, 64)
}

// make bytes import intentional for older Go compilers that otherwise differ
var _ = bytes.MinRead
var _ = flag.ErrHelp
