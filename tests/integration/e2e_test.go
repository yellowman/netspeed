//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const maxTransferBytes int64 = 16 << 20

type daemonProcess struct {
	command *exec.Cmd
	wait    chan error
	logPath string
	baseURL string
}

func TestDaemonHTTPAndCLI(t *testing.T) {
	root := repositoryRoot(t)
	binaries := buildBinaries(t, root)
	daemon := startDaemon(t, root, binaries.daemon, false)

	verifyHTTPContract(t, daemon.baseURL)
	verifyCLIResult(t, binaries.client, daemon.baseURL, "download")
	verifyCLIResult(t, binaries.client, daemon.baseURL, "upload")
}

func TestEmbeddedTURNPacketLoss(t *testing.T) {
	if os.Getenv("NETSPEED_E2E_TURN") != "1" {
		t.Skip("set NETSPEED_E2E_TURN=1 to run the real Pion/TURN interoperability test")
	}
	root := repositoryRoot(t)
	binaries := buildBinaries(t, root)
	daemon := startDaemon(t, root, binaries.daemon, true)

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaries.client,
		"--server", daemon.baseURL,
		"--quick",
		"--download-only",
		"--json",
		"--timeout", "50s",
	)
	command.Env = cleanEnvironment()
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errorAs(err, &exitErr); ok {
			t.Fatalf("packet-loss CLI failed: %v\nstderr:\n%s\ndaemon log:\n%s", err, exitErr.Stderr, readLog(daemon.logPath))
		}
		t.Fatalf("packet-loss CLI failed: %v\ndaemon log:\n%s", err, readLog(daemon.logPath))
	}
	var result struct {
		PacketLoss *struct {
			Sent           int  `json:"sent"`
			FrameSizeBytes int  `json:"frameSizeBytes"`
			Unavailable    bool `json:"unavailable"`
		} `json:"packetLoss"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode packet-loss CLI JSON: %v\noutput:\n%s", err, output)
	}
	if result.PacketLoss == nil || result.PacketLoss.Unavailable {
		t.Fatalf("packet-loss result is unavailable: %s", output)
	}
	if result.PacketLoss.Sent <= 0 || result.PacketLoss.FrameSizeBytes != 1200 {
		t.Fatalf("unexpected packet-loss counters: %s", output)
	}
}

type builtBinaries struct {
	client string
	daemon string
}

func buildBinaries(t *testing.T, root string) builtBinaries {
	t.Helper()
	directory := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binaries := builtBinaries{
		client: filepath.Join(directory, "netspeed"+suffix),
		daemon: filepath.Join(directory, "netspeedd"+suffix),
	}
	buildProgram(t, root, "./cmd/netspeed", binaries.client)
	buildProgram(t, root, "./cmd/netspeedd", binaries.daemon)
	return binaries
}

func buildProgram(t *testing.T, root, packagePath, output string) {
	t.Helper()
	command := exec.Command("go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", output, packagePath)
	command.Dir = root
	command.Env = cleanEnvironment()
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, combined)
	}
}

func startDaemon(t *testing.T, root, binary string, embeddedTURN bool) *daemonProcess {
	t.Helper()
	address := reserveAddress(t)
	logFile, err := os.CreateTemp(t.TempDir(), "netspeedd-*.log")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--listen", address,
		"--web-dir", filepath.Join(root, "web"),
		"--cors=false",
		"--max-bytes", fmt.Sprint(maxTransferBytes),
		"--max-transfers", "64",
		"--max-client-transfers", "16",
		"--client-quota-bytes", "0",
	}
	if embeddedTURN {
		args = append(args,
			"--embedded-turn",
			"--embedded-turn-addr", "127.0.0.1:0",
			"--embedded-turn-max-mbps", "1000",
		)
	}
	command := exec.Command(binary, args...)
	command.Dir = root
	command.Env = cleanEnvironment()
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start daemon: %v", err)
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
		_ = logFile.Close()
	}()
	process := &daemonProcess{
		command: command,
		wait:    wait,
		logPath: logFile.Name(),
		baseURL: "http://" + address,
	}
	t.Cleanup(func() { stopDaemon(t, process) })
	waitForHealth(t, process)
	return process
}

func stopDaemon(t *testing.T, process *daemonProcess) {
	t.Helper()
	select {
	case <-process.wait:
		return
	default:
	}
	_ = process.command.Process.Signal(os.Interrupt)
	select {
	case err := <-process.wait:
		if err != nil {
			t.Logf("daemon shutdown: %v\n%s", err, readLog(process.logPath))
		}
	case <-time.After(10 * time.Second):
		_ = process.command.Process.Kill()
		<-process.wait
		t.Errorf("daemon did not stop after interrupt\n%s", readLog(process.logPath))
	}
}

func waitForHealth(t *testing.T, process *daemonProcess) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(process.baseURL + "/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daemon failed health check\n%s", readLog(process.logPath))
}

func verifyHTTPContract(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second}

	response, err := client.Get(baseURL + "/meta")
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		MaxTransferBytes           int64 `json:"maxTransferBytes"`
		MeasurementProtocolVersion int   `json:"measurementProtocolVersion"`
		UploadReceiptVersion       int   `json:"uploadReceiptVersion"`
	}
	decodeResponse(t, response, http.StatusOK, &meta)
	if meta.MaxTransferBytes != maxTransferBytes || meta.MeasurementProtocolVersion != 2 || meta.UploadReceiptVersion != 1 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	const transferBytes = 128 * 1024
	response, err = client.Get(fmt.Sprintf("%s/__down?bytes=%d&measId=e2e-download", baseURL, transferBytes))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("download status %d: %s", response.StatusCode, body)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || len(body) != transferBytes {
		t.Fatalf("download bytes=%d error=%v", len(body), err)
	}
	if response.Header.Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("unexpected download cache policy: %q", response.Header.Get("Cache-Control"))
	}

	payload := bytes.Repeat([]byte{0xa5}, transferBytes)
	response, err = client.Post(baseURL+"/__up?measId=e2e-upload", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		OK               bool  `json:"ok"`
		AcceptedBytes    int64 `json:"acceptedBytes"`
		ServerDurationNS int64 `json:"serverDurationNs"`
	}
	decodeResponse(t, response, http.StatusOK, &receipt)
	if !receipt.OK || receipt.AcceptedBytes != transferBytes || receipt.ServerDurationNS <= 0 {
		t.Fatalf("invalid upload receipt: %+v", receipt)
	}

	request, err := http.NewRequest(http.MethodPost, baseURL+"/__up?measId=e2e-oversize", io.LimitReader(zeroReader{}, maxTransferBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = maxTransferBytes + 1
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("oversized upload status=%d body=%q", response.StatusCode, body)
	}
}

func verifyCLIResult(t *testing.T, binary, baseURL, direction string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	args := []string{
		"--server", baseURL,
		"--quick",
		"--no-packet-loss",
		"--json",
		"--timeout", "40s",
	}
	if direction == "download" {
		args = append(args, "--download-only")
	} else {
		args = append(args, "--upload-only")
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = cleanEnvironment()
	stdout, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errorAs(err, &exitErr) {
			t.Fatalf("%s CLI failed: %v\nstderr:\n%s", direction, err, exitErr.Stderr)
		}
		t.Fatalf("%s CLI failed: %v", direction, err)
	}
	var result struct {
		Meta struct {
			MeasurementProtocolVersion int `json:"measurementProtocolVersion"`
		} `json:"meta"`
		Summary struct {
			DownloadMbps      float64 `json:"downloadMbps"`
			UploadMbps        float64 `json:"uploadMbps"`
			LatencyUnloadedMs float64 `json:"latencyUnloadedMs"`
			LatencyDownloadMs float64 `json:"latencyDownloadMs"`
			LatencyUploadMs   float64 `json:"latencyUploadMs"`
		} `json:"summary"`
		ThroughputSamples []json.RawMessage `json:"throughputSamples"`
		PacketLoss        json.RawMessage   `json:"packetLoss"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		t.Fatalf("decode %s CLI JSON: %v\n%s", direction, err, stdout)
	}
	if result.Meta.MeasurementProtocolVersion != 2 || result.Summary.LatencyUnloadedMs <= 0 || len(result.ThroughputSamples) == 0 {
		t.Fatalf("incomplete %s result: %s", direction, stdout)
	}
	if direction == "download" {
		if result.Summary.DownloadMbps <= 0 || result.Summary.LatencyDownloadMs <= 0 || result.Summary.UploadMbps != 0 {
			t.Fatalf("invalid download-only summary: %s", stdout)
		}
	} else if result.Summary.UploadMbps <= 0 || result.Summary.LatencyUploadMs <= 0 || result.Summary.DownloadMbps != 0 {
		t.Fatalf("invalid upload-only summary: %s", stdout)
	}
	if strings.TrimSpace(string(result.PacketLoss)) != "null" {
		t.Fatalf("packet loss should be null when skipped: %s", result.PacketLoss)
	}
}

func decodeResponse(t *testing.T, response *http.Response, wantStatus int, destination any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d want=%d body=%q", response.StatusCode, wantStatus, body)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate integration test source")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func cleanEnvironment() []string {
	result := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(name, "NETSPEEDD_") || name == "NETSPEED_TOKEN" || name == "GOFLAGS" {
			continue
		}
		result = append(result, value)
	}
	return append(result, "GOTOOLCHAIN=local")
}

func readLog(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	const maximum = 64 * 1024
	if len(contents) > maximum {
		contents = contents[len(contents)-maximum:]
	}
	return string(contents)
}

func errorAs(err error, target any) bool {
	return errors.As(err, target)
}
