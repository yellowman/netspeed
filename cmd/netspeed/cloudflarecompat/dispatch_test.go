package cloudflarecompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProbePrefersRecognizableNetspeed(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/meta" {
			json.NewEncoder(w).Encode(map[string]any{"measurementProtocolVersion": 2, "maxTransferBytes": 1048576})
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	p, err := probeProvider(options{Server: s.URL, Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Netspeed || p.Cloudflare {
		t.Fatalf("unexpected probe: %+v", p)
	}
}
func TestProbeDoesNotDowngradeIncompatibleNetspeed(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/meta" {
			json.NewEncoder(w).Encode(map[string]any{"measurementProtocolVersion": 1, "maxTransferBytes": 1048576})
			return
		}
		w.Header().Set("Server", "cloudflare")
	}))
	defer s.Close()
	p, err := probeProvider(options{Server: s.URL, Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Incompatible || p.Cloudflare {
		t.Fatalf("unexpected probe: %+v", p)
	}
}
func TestProbeRequiresPositiveCloudflareFingerprint(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__down" {
			w.Header().Set("CF-Ray", "test")
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	p, err := probeProvider(options{Server: s.URL, Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Cloudflare {
		t.Fatalf("unexpected probe: %+v", p)
	}
}
func TestCloudflareExactDownloadAndCompleteUpload(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int64(0)
		if v := r.URL.Query().Get("bytes"); v != "" {
			fmtSscanf(v, &n)
		}
		if r.URL.Path == "/__down" {
			w.Write(make([]byte, n))
			return
		}
		if r.URL.Path == "/__up" {
			got, _ := ioCopyDiscard(r.Body)
			if got != n {
				t.Errorf("got %d want %d", got, n)
			}
			w.WriteHeader(200)
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	o := options{Server: s.URL, Timeout: 0}
	c := newHTTPClient(o)
	if n, _, e := transferOnce(context.Background(), c, o, false, 32768); e != nil || n != 32768 {
		t.Fatalf("download n=%d err=%v", n, e)
	}
	if n, _, e := transferOnce(context.Background(), c, o, true, 32768); e != nil || n != 32768 {
		t.Fatalf("upload n=%d err=%v", n, e)
	}
}

// Tiny wrappers avoid importing fmt/io solely for test fixture expressions in generated trees.
func fmtSscanf(s string, n *int64) {
	var v int64
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int64(c-'0')
	}
	*n = v
}
func ioCopyDiscard(r io.Reader) (int64, error) { return io.Copy(io.Discard, r) }

func TestCloudflareRunEmitsIdentifiedResult(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__down" {
			w.Header().Set("CF-Ray", "fixture")
			n := int64(0)
			fmtSscanf(r.URL.Query().Get("bytes"), &n)
			_, _ = w.Write(make([]byte, n))
			return
		}
		if r.URL.Path == "/__up" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := runCloudflare(options{Provider: providerCloudflare, Server: s.URL, JSON: true, Quick: true, DownloadOnly: true, SkipPacketLoss: true, Timeout: 10 * time.Second})
	_ = w.Close()
	os.Stdout = old
	body, _ := io.ReadAll(r)
	_ = r.Close()
	if code != 0 {
		t.Fatalf("runCloudflare exit=%d output=%s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, body)
	}
	if got["provider"] != "cloudflare" || got["measurementContract"] != "cloudflare-http-v2" || got["packetTopology"] != "turn-loopback" {
		t.Fatalf("missing provider identity: %#v", got)
	}
	transport, ok := got["httpTransport"].(map[string]any)
	parameters, parametersOK := transport["compatibilityQueryParameters"].([]any)
	if !ok || transport["capabilitySource"] != "behavioral-probe" ||
		transport["privateTransportDiscriminatorsSent"] != false ||
		!parametersOK || len(parameters) != 6 {
		t.Fatalf("missing transport evidence: %#v", got["httpTransport"])
	}
	latency, ok := got["latency"].(map[string]any)
	if !ok || latency["connectionReused"] != true || latency["probePath"] != "/__down" {
		t.Fatalf("missing warm latency evidence: %#v", got["latency"])
	}
}

func TestParseProviderContract(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		provider string
		server   string
		quick    bool
		skip     bool
	}{
		{name: "explicit netspeed", args: []string{"--provider", "netspeed", "https://strict.example"}, provider: providerNetspeed, server: "https://strict.example"},
		{name: "explicit cloudflare", args: []string{"--provider=cloudflare", "https://speed.cloudflare.com"}, provider: providerCloudflare, server: "https://speed.cloudflare.com"},
		{name: "auto positional and quick shorthand", args: []string{"-q", "https://edge.example"}, provider: providerAuto, server: "https://edge.example", quick: true},
		{name: "packet skip canonical", args: []string{"--no-packet-loss"}, provider: providerAuto, server: "http://localhost:8080", skip: true},
		{name: "packet skip compatibility alias", args: []string{"--skip-packet-loss"}, provider: providerAuto, server: "http://localhost:8080", skip: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, stripped, err := parseOptions(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if o.Provider != tc.provider || o.Server != tc.server || o.Quick != tc.quick || o.SkipPacketLoss != tc.skip {
				t.Fatalf("options=%+v", o)
			}
			for _, arg := range stripped {
				if arg == "--provider" || arg == "netspeed" || arg == "cloudflare" {
					t.Fatalf("provider option leaked to strict parser: %q in %#v", arg, stripped)
				}
			}
		})
	}
}

func TestParseProviderRejectsAmbiguousPositionalServer(t *testing.T) {
	_, _, err := parseOptions([]string{"https://one.example", "https://two.example"})
	if err == nil {
		t.Fatal("expected second positional server to be rejected")
	}
}

func TestParseProviderRejectsOutputModeConflict(t *testing.T) {
	_, _, err := parseOptions([]string{"--json", "--quiet"})
	if err == nil {
		t.Fatal("expected machine output conflict to be rejected")
	}
}

func TestDispatchHelpStripsCompatibilityOptions(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"netspeed"}
	handled, code := Dispatch([]string{"--provider", "cloudflare", "--turn-url", "turn:example.test", "--help"})
	if handled || code != 0 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	if len(os.Args) != 2 || os.Args[1] != "--help" {
		t.Fatalf("help args=%#v", os.Args)
	}
}

func TestDispatchExplicitNetspeedUsesStrictClient(t *testing.T) {
	oldArgs := os.Args
	oldProvider := os.Getenv("NETSPEED_SELECTED_PROVIDER")
	defer func() {
		os.Args = oldArgs
		_ = os.Setenv("NETSPEED_SELECTED_PROVIDER", oldProvider)
	}()
	os.Args = []string{"netspeed"}
	handled, code := Dispatch([]string{"--provider", "netspeed", "https://strict.example", "--json"})
	if handled || code != 0 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	if got := os.Getenv("NETSPEED_SELECTED_PROVIDER"); got != providerNetspeed {
		t.Fatalf("selected provider=%q", got)
	}
	if len(os.Args) != 3 || os.Args[1] != "https://strict.example" || os.Args[2] != "--json" {
		t.Fatalf("strict args=%#v", os.Args)
	}
}

func TestDispatchAutoRefusesIncompatibleNetspeedDowngrade(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("CF-Ray", "proxy-added-header")
		if r.URL.Path == "/meta" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"measurementProtocolVersion": 1,
				"uploadReceiptVersion":       1,
				"maxTransferBytes":           1048576,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()

	oldArgs := os.Args
	oldProvider := os.Getenv("NETSPEED_SELECTED_PROVIDER")
	defer func() {
		os.Args = oldArgs
		_ = os.Setenv("NETSPEED_SELECTED_PROVIDER", oldProvider)
	}()
	os.Args = []string{"netspeed"}
	handled, code := Dispatch([]string{"--provider", "auto", "--server", s.URL, "--json"})
	if handled || code != 0 {
		t.Fatalf("incompatible Netspeed endpoint was downgraded: handled=%v code=%d", handled, code)
	}
	if got := os.Getenv("NETSPEED_SELECTED_PROVIDER"); got != providerNetspeed {
		t.Fatalf("selected provider=%q", got)
	}
}

func TestParseOptionsPreservesStrictTransportControls(t *testing.T) {
	o, stripped, err := parseOptions([]string{
		"--provider", "netspeed",
		"--download-payload", "zero",
		"--download-framing=chunked",
		"--download-chunk-bytes", "4096",
		"--download-flush=false",
		"https://strict.example",
	})
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if !o.TransportControls || o.Server != "https://strict.example" || o.DownloadPayload != "zero" || o.DownloadFraming != "chunked" || o.DownloadChunkBytes != 4096 || o.DownloadFlush != "false" {
		t.Fatalf("options = %+v; want parsed transport controls and positional server", o)
	}
	joined := strings.Join(stripped, " ")
	for _, expected := range []string{
		"--download-payload zero",
		"--download-framing=chunked",
		"--download-chunk-bytes 4096",
		"--download-flush=false",
		"https://strict.example",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("stripped args = %#v; want %q", stripped, expected)
		}
	}
}

func TestParseOptionsTreatsAutomaticTransportValuesAsNoOp(t *testing.T) {
	o, _, err := parseOptions([]string{
		"--download-payload", "auto",
		"--download-framing=auto",
		"--download-chunk-bytes", "0",
		"--download-flush=auto",
	})
	if err != nil {
		t.Fatalf("parseOptions: %v", err)
	}
	if o.TransportControls {
		t.Fatalf("automatic transport values were classified as explicit: %+v", o)
	}
}

func TestParseOptionsRejectsInvalidTransportControls(t *testing.T) {
	for _, args := range [][]string{
		{"--download-payload", "compressible"},
		{"--download-framing", "h2"},
		{"--download-chunk-bytes", "-1"},
		{"--download-flush", "sometimes"},
	} {
		if _, _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) unexpectedly succeeded", args)
		}
	}
}

func TestDispatchCloudflareTransportControlMismatchIsArgumentError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/__down" {
			http.NotFound(writer, request)
			return
		}
		var count int64
		fmtSscanf(request.URL.Query().Get("bytes"), &count)
		writer.Header().Set("Content-Length", request.URL.Query().Get("bytes"))
		_, _ = writer.Write(make([]byte, count))
	}))
	defer server.Close()

	oldArgs := os.Args
	oldStderr := os.Stderr
	defer func() {
		os.Args = oldArgs
		os.Stderr = oldStderr
	}()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	os.Args = []string{"netspeed"}
	handled, code := Dispatch([]string{"--provider", "cloudflare", "--server", server.URL, "--download-payload", "random", "--download-only", "--quick", "--no-packet-loss"})
	_ = writer.Close()
	body, _ := io.ReadAll(reader)
	_ = reader.Close()

	if !handled || code != 2 {
		t.Fatalf("handled=%v code=%d; want handled argument error", handled, code)
	}
	if !strings.Contains(string(body), "provider-default payload") || !strings.Contains(string(body), "cannot be honored") {
		t.Fatalf("stderr = %q; want observed-default mismatch guidance", body)
	}
}
