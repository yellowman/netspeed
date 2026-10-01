package cloudflarecompat

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestStrictOptionsAfterPositionalURL(t *testing.T) {
	_, args, err := parseOptions([]string{"--provider", "netspeed", "https://server.test", "--json", "--timeout", "7s", "--download-framing=chunked"})
	if err != nil {
		t.Fatal(err)
	}
	flags := flag.NewFlagSet("netspeed", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "")
	timeout := flags.Duration("timeout", 0, "")
	framing := flags.String("download-framing", "", "")
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	if !*jsonOutput || *timeout != 7*time.Second || *framing != "chunked" || flags.Arg(0) != "https://server.test" {
		t.Fatalf("parsed JSON=%v timeout=%v framing=%v URL=%v", *jsonOutput, *timeout, *framing, flags.Args())
	}
}

func TestProviderVersionShorthandDoesNotMeasure(t *testing.T) {
	if !hasHelpOrVersion([]string{"--provider", "cloudflare", "-V"}) {
		t.Fatal("-V must bypass measurement")
	}
}

func TestCloudflareHeadlineAggregatesWorkers(t *testing.T) {
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "download", true: "upload"}[upload], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
				block := make([]byte, 16<<10)
				if r.Method == http.MethodPost {
					for n > 0 {
						count := len(block)
						if n < count {
							count = n
						}
						if _, err := io.ReadFull(r.Body, block[:count]); err != nil {
							return
						}
						n -= count
						time.Sleep(3 * time.Millisecond)
					}
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(n))
				w.WriteHeader(http.StatusOK)
				for n > 0 {
					count := len(block)
					if n < count {
						count = n
					}
					if _, err := w.Write(block[:count]); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					n -= count
					time.Sleep(3 * time.Millisecond)
				}
			}))
			defer server.Close()
			opts := options{Server: server.URL, Timeout: 5 * time.Second, Quick: true}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			client := newHTTPClient(opts)
			defer client.CloseIdleConnections()
			result, _ := measureDirection(ctx, client, opts, upload)
			if !result.Available {
				t.Fatalf("measurement failed: %+v", result)
			}
			individual := percentile(result.Samples, .90)
			if *result.Mbps < individual*1.4 {
				t.Fatalf("aggregate %.2f Mbps versus individual %.2f Mbps", *result.Mbps, individual)
			}
			want := float64(result.WindowBytes) * 8 / result.WindowSeconds / 1e6
			if *result.Mbps != want {
				t.Fatalf("headline %v differs from aggregate %v", *result.Mbps, want)
			}
		})
	}
}

func TestCloudflareWindowExcludesWarmupAndLateBytes(t *testing.T) {
	var warmupDone atomic.Bool
	const initialBytes = 64 << 10
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		if n == 0 {
			return // Warm and loaded latency probes.
		}
		if !warmupDone.Swap(true) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write(make([]byte, n))
			return
		}
		_, _ = w.Write(make([]byte, initialBytes))
		w.(http.Flusher).Flush()
		// These bytes complete and validate the response after the 900ms
		// window. They must not inflate its headline throughput.
		time.Sleep(1100 * time.Millisecond)
		_, _ = w.Write(make([]byte, n-initialBytes))
	}))
	defer server.Close()
	opts := options{Server: server.URL, Timeout: 5 * time.Second, Quick: true}
	client := newHTTPClient(opts)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, _ := measureDirection(ctx, client, opts, false)
	if !result.Available || result.WindowBytes != 2*initialBytes || result.WindowSeconds != .9 {
		t.Fatalf("window included warmup or late bytes: %+v", result)
	}
}
