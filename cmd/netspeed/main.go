// Command netspeed is the CLI client for a netspeedd measurement server.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yellowman/netspeed/cmd/netspeed/client"
	compatclient "github.com/yellowman/netspeed/cmd/netspeed/cloudflarecompat"
	"github.com/yellowman/netspeed/cmd/netspeed/output"
	"github.com/yellowman/netspeed/internal/buildinfo"
)

func main() {
	if handled, code := compatclient.Dispatch(os.Args[1:]); handled {
		os.Exit(code)
	}
	// Command line flags
	var (
		serverURL       string
		accessToken     string
		jsonOutput      bool
		csvOutput       bool
		quiet           bool
		verbose         bool
		quick           bool
		downloadOnly    bool
		uploadOnly      bool
		noPacketLoss    bool
		downloadPayload string
		downloadFraming string
		downloadChunk   int
		downloadFlush   string
		noColor         bool
		timeout         time.Duration
		showVersion     bool
	)

	flag.StringVar(&serverURL, "server", "", "Server URL (default: http://localhost:8080)")
	flag.StringVar(&serverURL, "s", "", "Server URL (shorthand)")
	flag.StringVar(&accessToken, "token", "", "Shared bearer token (or NETSPEED_TOKEN)")
	flag.BoolVar(&jsonOutput, "json", false, "Output results as JSON")
	flag.BoolVar(&jsonOutput, "j", false, "Output results as JSON (shorthand)")
	flag.BoolVar(&csvOutput, "csv", false, "Output results as CSV")
	flag.BoolVar(&quiet, "quiet", false, "Minimal output (final results only)")
	flag.BoolVar(&verbose, "verbose", false, "Show detailed progress")
	flag.BoolVar(&verbose, "v", false, "Show detailed progress (shorthand)")
	flag.BoolVar(&quick, "quick", false, "Quick test mode (fewer samples)")
	flag.BoolVar(&quick, "q", false, "Quick test mode (shorthand)")
	flag.BoolVar(&downloadOnly, "download-only", false, "Skip upload tests")
	flag.BoolVar(&downloadOnly, "d", false, "Skip upload tests (shorthand)")
	flag.BoolVar(&uploadOnly, "upload-only", false, "Skip download tests")
	flag.BoolVar(&uploadOnly, "u", false, "Skip download tests (shorthand)")
	flag.BoolVar(&noPacketLoss, "no-packet-loss", false, "Skip packet loss test")
	flag.StringVar(&downloadPayload, "download-payload", "auto", "Download payload: auto, random, or zero")
	flag.StringVar(&downloadFraming, "download-framing", "auto", "Download framing: auto, fixed, or chunked")
	flag.IntVar(&downloadChunk, "download-chunk-bytes", 0, "Daemon application chunk size; 0 uses the advertised default")
	flag.StringVar(&downloadFlush, "download-flush", "auto", "Per-chunk flush: auto, true, or false")
	flag.BoolVar(&noColor, "no-color", false, "Disable colored output")
	flag.DurationVar(&timeout, "timeout", 60*time.Second, "Total test timeout")
	flag.DurationVar(&timeout, "t", 60*time.Second, "Total test timeout (shorthand)")
	flag.BoolVar(&showVersion, "version", false, "Show version and exit")
	flag.BoolVar(&showVersion, "V", false, "Show version (shorthand)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: netspeed [flags] [server-url]\n\n")
		fmt.Fprintf(os.Stderr, "A command-line speed test client for netspeedd.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		compatclient.WriteUsage(os.Stderr)
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  netspeed                           Run test against http://localhost:8080\n")
		fmt.Fprintf(os.Stderr, "  netspeed https://speed.example.com Run test against specific server\n")
		fmt.Fprintf(os.Stderr, "  netspeed --quick                   Quick test with fewer samples\n")
		fmt.Fprintf(os.Stderr, "  netspeed --json                    Output results as JSON\n")
		fmt.Fprintf(os.Stderr, "  netspeed -v                        Verbose output with details\n")
		fmt.Fprintf(os.Stderr, "  netspeed --provider cloudflare https://speed.cloudflare.com\n")
	}

	flag.Parse()

	if showVersion {
		fmt.Println(buildinfo.Line("netspeed"))
		os.Exit(0)
	}

	// Get server URL from positional argument if not specified via flag
	if serverURL == "" && flag.NArg() > 0 {
		serverURL = flag.Arg(0)
	}

	// Default server if not specified
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}

	// Normalize server URL and load optional authentication from the environment.
	serverURL = strings.TrimSuffix(serverURL, "/")
	if accessToken == "" {
		accessToken = os.Getenv("NETSPEED_TOKEN")
	}

	// Detect if running in interactive terminal
	interactive := output.IsInteractive() && !jsonOutput && !csvOutput && !quiet

	// Disable color if requested or not interactive
	useColor := !noColor && interactive

	// Create output formatter
	out := output.New(output.Config{
		JSON:        jsonOutput,
		CSV:         csvOutput,
		Quiet:       quiet,
		Verbose:     verbose,
		Color:       useColor,
		Interactive: interactive,
	})

	// Create client config
	cfg := client.Config{
		ServerURL:          serverURL,
		Timeout:            timeout,
		Quick:              quick,
		DownloadOnly:       downloadOnly,
		UploadOnly:         uploadOnly,
		SkipPacketLoss:     noPacketLoss,
		AccessToken:        accessToken,
		DownloadPayload:    downloadPayload,
		DownloadFraming:    downloadFraming,
		DownloadChunkBytes: downloadChunk,
		DownloadFlush:      downloadFlush,
		OnProgress: func(stage string, current, total int, value float64) {
			out.Progress(stage, current, total, value)
		},
	}

	// Create speed test client
	c := client.New(cfg)

	// Set up context with timeout and signal handling
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		out.Error("Test interrupted")
		cancel()
	}()

	// Print header
	out.Header(serverURL, buildinfo.Version)

	// Run the speed test
	results, err := c.Run(ctx)
	if err != nil {
		out.Error(err.Error())
		os.Exit(1)
	}

	// Output results
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results.ToJSON())
	} else if csvOutput {
		out.CSV(results)
	} else if quiet {
		out.Quiet(results)
	} else {
		out.Results(results)
	}
}
