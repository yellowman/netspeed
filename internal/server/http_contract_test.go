package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/clientaddr"
	"github.com/yellowman/netspeed/internal/config"
)

func TestResponseWriterRecordsFirstStatusAndImplicitWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := newResponseWriter(recorder)
	writer.WriteHeader(http.StatusCreated)
	writer.WriteHeader(http.StatusInternalServerError)
	if _, err := writer.Write([]byte("ok")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if writer.StatusCode() != http.StatusCreated || recorder.Code != http.StatusCreated {
		t.Fatalf("status tracker=%d recorder=%d; want 201", writer.StatusCode(), recorder.Code)
	}
	if writer.BytesWritten() != 2 || recorder.Body.String() != "ok" {
		t.Fatalf("bytes=%d body=%q", writer.BytesWritten(), recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	writer = newResponseWriter(recorder)
	if _, err := writer.Write([]byte("implicit")); err != nil {
		t.Fatalf("implicit write: %v", err)
	}
	if writer.StatusCode() != http.StatusOK || recorder.Code != http.StatusOK {
		t.Fatalf("implicit status tracker=%d recorder=%d; want 200", writer.StatusCode(), recorder.Code)
	}
}

type optionalWriter struct {
	header http.Header
	body   strings.Builder

	status          int
	flushed         bool
	pushed          string
	readDeadlines   []time.Time
	writeDeadlines  []time.Time
	fullDuplex      bool
	readerFromCalls int

	hijackClient net.Conn
	hijackServer net.Conn
}

func newOptionalWriter() *optionalWriter {
	client, server := net.Pipe()
	return &optionalWriter{header: make(http.Header), hijackClient: client, hijackServer: server}
}

func (w *optionalWriter) Header() http.Header               { return w.header }
func (w *optionalWriter) WriteHeader(code int)              { w.status = code }
func (w *optionalWriter) Write(payload []byte) (int, error) { return w.body.Write(payload) }
func (w *optionalWriter) Flush()                            { w.flushed = true }
func (w *optionalWriter) Push(target string, _ *http.PushOptions) error {
	w.pushed = target
	return nil
}
func (w *optionalWriter) SetReadDeadline(value time.Time) error {
	w.readDeadlines = append(w.readDeadlines, value)
	return nil
}
func (w *optionalWriter) SetWriteDeadline(value time.Time) error {
	w.writeDeadlines = append(w.writeDeadlines, value)
	return nil
}
func (w *optionalWriter) EnableFullDuplex() error { w.fullDuplex = true; return nil }
func (w *optionalWriter) ReadFrom(reader io.Reader) (int64, error) {
	w.readerFromCalls++
	return io.Copy(&w.body, reader)
}
func (w *optionalWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijackServer, bufio.NewReadWriter(bufio.NewReader(w.hijackServer), bufio.NewWriter(w.hijackServer)), nil
}

func TestResponseWriterDelegatesStreamingAndControllerCapabilities(t *testing.T) {
	underlying := newOptionalWriter()
	defer underlying.hijackClient.Close()
	defer underlying.hijackServer.Close()
	writer := newResponseWriter(underlying)

	if _, err := writer.ReadFrom(strings.NewReader("streamed")); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if underlying.readerFromCalls != 1 || writer.BytesWritten() != int64(len("streamed")) {
		t.Fatalf("ReaderFrom calls=%d bytes=%d", underlying.readerFromCalls, writer.BytesWritten())
	}
	if err := writer.FlushError(); err != nil || !underlying.flushed {
		t.Fatalf("FlushError err=%v flushed=%v", err, underlying.flushed)
	}
	if err := writer.Push("/asset.js", nil); err != nil || underlying.pushed != "/asset.js" {
		t.Fatalf("Push err=%v target=%q", err, underlying.pushed)
	}
	deadline := time.Now().Add(time.Second)
	if err := writer.SetReadDeadline(deadline); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if err := writer.SetWriteDeadline(deadline); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}
	if err := writer.EnableFullDuplex(); err != nil || !underlying.fullDuplex {
		t.Fatalf("EnableFullDuplex err=%v enabled=%v", err, underlying.fullDuplex)
	}
	connection, _, err := writer.Hijack()
	if err != nil || connection == nil {
		t.Fatalf("Hijack connection=%v err=%v", connection, err)
	}
}

func TestRecoveryWrites500OnlyBeforeCommit(t *testing.T) {
	server := measurementTestServer(1024)
	uncommitted := server.recoveryMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("before commit")
	}))
	recorder := httptest.NewRecorder()
	uncommitted.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/meta", nil))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "Internal Server Error") {
		t.Fatalf("uncommitted panic status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	committedRecorder := httptest.NewRecorder()
	tracked := newResponseWriter(committedRecorder)
	committed := server.recoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("after commit")
	}))
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		committed.ServeHTTP(tracked, httptest.NewRequest(http.MethodGet, "/__down", nil))
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("committed panic recovered=%v; want http.ErrAbortHandler", recovered)
	}
	if strings.Contains(committedRecorder.Body.String(), "Internal Server Error") {
		t.Fatalf("committed response was corrupted with a replacement error: %q", committedRecorder.Body.String())
	}
}

func TestCORSCrossOriginTimingAndCredentialsContract(t *testing.T) {
	server := measurementTestServer(1024)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	wildcard := server.corsMiddleware(next)
	request := httptest.NewRequest(http.MethodGet, "/meta", nil)
	request.Header.Set("Origin", "https://ui.example.test")
	recorder := httptest.NewRecorder()
	wildcard.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" || recorder.Header().Get("Timing-Allow-Origin") != "*" {
		t.Fatalf("wildcard CORS=%q TAO=%q", recorder.Header().Get("Access-Control-Allow-Origin"), recorder.Header().Get("Timing-Allow-Origin"))
	}
	exposed := recorder.Header().Get("Access-Control-Expose-Headers")
	for _, header := range []string{"Content-Encoding", "Server-Timing", "X-Accel-Buffering", "X-Netspeed-Payload", "X-Netspeed-Framing", "X-Netspeed-Flush"} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("exposed headers=%q; want %s", exposed, header)
		}
	}

	server.cfg.AllowedOrigins = []string{"https://UI.Example.Test"}
	server.cfg.CORSAllowCredentials = true
	credentialed := server.corsMiddleware(next)
	recorder = httptest.NewRecorder()
	credentialed.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://ui.example.test" ||
		recorder.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		recorder.Header().Get("Timing-Allow-Origin") != "https://ui.example.test" {
		t.Fatalf("credentialed headers: %#v", recorder.Header())
	}
	if !strings.Contains(strings.Join(recorder.Header().Values("Vary"), ","), "Origin") {
		t.Fatalf("Vary=%v; want Origin", recorder.Header().Values("Vary"))
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/meta", nil)
	preflight.Header.Set("Origin", "https://ui.example.test")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodHead)
	preflight.Header.Set("Access-Control-Request-Headers", "authorization, cache-control, content-encoding")
	recorder = httptest.NewRecorder()
	credentialed.ServeHTTP(recorder, preflight)
	if recorder.Code != http.StatusNoContent || !strings.Contains(recorder.Header().Get("Access-Control-Allow-Headers"), "Cache-Control") ||
		!strings.Contains(recorder.Header().Get("Access-Control-Allow-Headers"), "Content-Encoding") ||
		!strings.Contains(recorder.Header().Get("Access-Control-Allow-Methods"), http.MethodHead) {
		t.Fatalf("preflight status=%d headers=%#v", recorder.Code, recorder.Header())
	}

	for _, method := range []string{http.MethodOptions, http.MethodGet} {
		disallowed := httptest.NewRequest(method, "/meta", nil)
		disallowed.Header.Set("Origin", "https://evil.example.test")
		if method == http.MethodOptions {
			disallowed.Header.Set("Access-Control-Request-Method", http.MethodGet)
		}
		recorder = httptest.NewRecorder()
		credentialed.ServeHTTP(recorder, disallowed)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("disallowed %s status=%d; want 403", method, recorder.Code)
		}
	}
}

func TestEndpointDeadlineSetsContextAndClearsConnectionDeadlines(t *testing.T) {
	server := measurementTestServer(1024)
	underlying := newOptionalWriter()
	defer underlying.hijackClient.Close()
	defer underlying.hijackServer.Close()
	tracked := newResponseWriter(underlying)

	handler := server.withEndpointDeadline(250*time.Millisecond, true, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 300*time.Millisecond {
			t.Fatalf("request deadline=%v ok=%v", deadline, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(tracked, httptest.NewRequest(http.MethodPost, "/__up", nil))

	if len(underlying.readDeadlines) < 2 || len(underlying.writeDeadlines) < 2 {
		t.Fatalf("read deadlines=%v write deadlines=%v", underlying.readDeadlines, underlying.writeDeadlines)
	}
	if !underlying.readDeadlines[len(underlying.readDeadlines)-1].IsZero() ||
		!underlying.writeDeadlines[len(underlying.writeDeadlines)-1].IsZero() {
		t.Fatalf("deadlines were not cleared: read=%v write=%v", underlying.readDeadlines, underlying.writeDeadlines)
	}
}

func TestShutdownDrainsHandlersBeforeDependencies(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	dependencyClosed := make(chan struct{})
	var closeOnce sync.Once

	server := &Server{
		metrics: &serviceMetrics{},
		dependencyCloser: func() error {
			closeOnce.Do(func() { close(dependencyClosed) })
			return nil
		},
	}
	server.httpServer = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.httpServer.Serve(listener) }()

	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(ctx)
	}()
	select {
	case <-dependencyClosed:
		t.Fatal("dependency closed before active handler drained")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-dependencyClosed:
	case <-time.After(time.Second):
		t.Fatal("dependency was not closed after handler drain")
	}
	if err := <-requestDone; err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve error=%v; want ErrServerClosed", err)
	}
}

func TestShutdownTimeoutLeavesDependenciesOpenForRetry(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	dependencyClosed := make(chan struct{})
	server := &Server{
		metrics: &serviceMetrics{},
		dependencyCloser: func() error {
			close(dependencyClosed)
			return nil
		},
	}
	server.httpServer = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.httpServer.Serve(listener) }()
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = server.Shutdown(ctx)
	cancel()
	if err == nil {
		t.Fatal("Shutdown unexpectedly succeeded while handler was blocked")
	}
	select {
	case <-dependencyClosed:
		t.Fatal("dependency closed after failed drain")
	default:
	}

	close(release)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
	select {
	case <-dependencyClosed:
	case <-time.After(time.Second):
		t.Fatal("dependency not closed after successful retry")
	}
}

func TestNewRejectsUnreadableTLSPairBeforeServing(t *testing.T) {
	cfg := config.Default()
	cfg.TLSCertFile = "/definitely/missing/netspeed-cert.pem"
	cfg.TLSKeyFile = "/definitely/missing/netspeed-key.pem"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "load TLS certificate/key pair") {
		t.Fatalf("New error=%v; want TLS pair load failure", err)
	}
}

func TestNewInstallsEndpointAwareHTTPTimeoutPolicy(t *testing.T) {
	cfg := config.Default()
	server, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	if server.httpServer.ReadHeaderTimeout != cfg.ReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout=%s; want %s", server.httpServer.ReadHeaderTimeout, cfg.ReadHeaderTimeout)
	}
	if server.httpServer.ReadTimeout != 0 || server.httpServer.WriteTimeout != 0 {
		t.Fatalf("whole-request timeouts read=%s write=%s; want disabled", server.httpServer.ReadTimeout, server.httpServer.WriteTimeout)
	}
	if server.httpServer.IdleTimeout != cfg.IdleTimeout {
		t.Fatalf("IdleTimeout=%s; want %s", server.httpServer.IdleTimeout, cfg.IdleTimeout)
	}
}

func TestNewRejectsConfiguredGeoIPDatabaseFailure(t *testing.T) {
	cfg := config.Default()
	cfg.GeoIPCityDatabasePath = "/definitely/missing/netspeed-city.mmdb"
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "load GeoIP databases") {
		t.Fatalf("New error=%v; want configured GeoIP failure", err)
	}
}

func TestFallbackMetadataDoesNotInventLocation(t *testing.T) {
	cfg := config.Default()
	resolver, err := clientaddr.NewResolver(nil)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	provider, closer, err := buildMetaProvider(cfg, resolver)
	if err != nil {
		t.Fatalf("buildMetaProvider: %v", err)
	}
	if closer != nil {
		t.Fatal("fallback provider unexpectedly returned a closer")
	}
	request := httptest.NewRequest(http.MethodGet, "/meta", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	metadata := provider.MetaFor(request)
	if metadata.Country != "" || metadata.City != "" || metadata.Region != "" || metadata.Timezone != "" || metadata.ASOrg != "" {
		t.Fatalf("fallback metadata invented location/network values: %+v", metadata)
	}
}
