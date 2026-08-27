package cloudflarecompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	if got["provider"] != "cloudflare" || got["measurementContract"] != "cloudflare-http-v1" || got["packetTopology"] != "turn-loopback" {
		t.Fatalf("missing provider identity: %#v", got)
	}
}
