package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/yellowman/netspeed/internal/meta"
)

// withEndpointDeadline applies a lifetime appropriate to one endpoint class.
// The net/http server itself has no whole-request ReadTimeout or WriteTimeout,
// because those absolute connection deadlines make valid slow transfers fail.
// Deadlines are cleared before a keep-alive connection is returned to the pool.
func (s *Server) withEndpointDeadline(timeout time.Duration, setRead, setWrite bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if timeout <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		controller := http.NewResponseController(w)
		deadline := time.Now().Add(timeout)
		if setRead {
			_ = ignoreUnsupported(controller.SetReadDeadline(deadline))
		}
		if setWrite {
			_ = ignoreUnsupported(controller.SetWriteDeadline(deadline))
		}
		defer func() {
			if setRead {
				_ = ignoreUnsupported(controller.SetReadDeadline(time.Time{}))
			}
			if setWrite {
				_ = ignoreUnsupported(controller.SetWriteDeadline(time.Time{}))
			}
		}()

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ignoreUnsupported(err error) error {
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

// corsMiddleware implements an explicit cross-origin contract for a separately
// hosted browser UI. Resource Timing visibility is granted with
// Timing-Allow-Origin on the same origin decision as CORS.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		addVary(w.Header(), "Origin")
		allowed, wildcard := s.allowedOrigin(origin)
		if !allowed {
			// Reject both preflight and actual browser-origin requests. Merely
			// omitting CORS response headers would still allow a hostile page to
			// consume measurement bandwidth with no ability to read the result.
			http.Error(w, "CORS origin is not allowed", http.StatusForbidden)
			return
		}

		allowOrigin := origin
		if wildcard && !s.cfg.CORSAllowCredentials {
			allowOrigin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Timing-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Expose-Headers", strings.Join([]string{
			"CDN-Cache-Control",
			"Content-Length",
			"Retry-After",
			"Surrogate-Control",
			"Server-Timing",
			"X-Accel-Buffering",
			"X-Netspeed-Accepted-Bytes",
			"X-Netspeed-Chunk-Bytes",
			"X-Netspeed-Content-Encoding",
			"X-Netspeed-Expected-Bytes",
			"X-Netspeed-Framing",
			"X-Netspeed-Measurement",
			"X-Netspeed-Payload",
			"X-Netspeed-Quota-Remaining-Bytes",
			"X-Netspeed-Upload-Duration-Ns",
			"cf-meta-asn",
			"cf-meta-city",
			"cf-meta-colo",
			"cf-meta-country",
			"cf-meta-ip",
			"cf-meta-latitude",
			"cf-meta-longitude",
			"cf-meta-postalcode",
			"cf-meta-request-time",
			"cf-meta-timezone",
		}, ", "))
		if s.cfg.CORSAllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if r.Method == http.MethodOptions {
			requestedMethod := strings.ToUpper(strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")))
			if requestedMethod != "" && requestedMethod != http.MethodGet && requestedMethod != http.MethodHead && requestedMethod != http.MethodPost && requestedMethod != http.MethodOptions {
				http.Error(w, "CORS method is not allowed", http.StatusMethodNotAllowed)
				return
			}
			addVary(w.Header(), "Access-Control-Request-Method")
			addVary(w.Header(), "Access-Control-Request-Headers")
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Cache-Control, Content-Type, Pragma, X-Requested-With")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) allowedOrigin(origin string) (allowed, wildcard bool) {
	incoming, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || incoming.Scheme == "" || incoming.Host == "" || incoming.User != nil ||
		incoming.Path != "" || incoming.RawQuery != "" || incoming.Fragment != "" {
		return false, false
	}
	for _, configured := range s.cfg.AllowedOrigins {
		configured = strings.TrimSpace(configured)
		if configured == "*" {
			return true, true
		}
		parsed, err := url.Parse(configured)
		if err == nil && strings.EqualFold(parsed.Scheme, incoming.Scheme) && strings.EqualFold(parsed.Host, incoming.Host) {
			return true, false
		}
	}
	return false, false
}

func addVary(header http.Header, value string) {
	for _, line := range header.Values("Vary") {
		for _, existing := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

// loggingMiddleware installs the one tracking writer used by all inner
// middleware. Its deferred log runs even when recovery aborts a committed HTTP/1
// stream with http.ErrAbortHandler.
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.metrics.httpRequests.Add(1)
		s.metrics.httpActive.Add(1)
		defer s.metrics.httpActive.Add(-1)

		tracked := newResponseWriter(w)
		defer func() {
			client := "unknown"
			if s.clientAddress != nil {
				client = s.clientIP(r)
			}
			log.Printf("%s %s %d %dB %s %s",
				r.Method,
				r.URL.Path,
				tracked.StatusCode(),
				tracked.BytesWritten(),
				time.Since(start),
				client,
			)
		}()
		next.ServeHTTP(tracked, r)
	})
}

// recoveryMiddleware sends a 500 only while the response is uncommitted. Once
// headers or body bytes have been sent, replacing the response is impossible;
// aborting the stream is safer than appending an error body to measured bytes.
func (s *Server) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			s.metrics.internalFailures.Add(1)
			log.Printf("Panic recovered for %s %s: %v\n%s", r.Method, r.URL.Path, recovered, debug.Stack())
			if responseCommitted(w) {
				panic(http.ErrAbortHandler)
			}
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

func responseCommitted(w http.ResponseWriter) bool {
	for w != nil {
		if tracker, ok := w.(interface{ Committed() bool }); ok {
			return tracker.Committed()
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = unwrapper.Unwrap()
	}
	return false
}

// responseWriter records only the first committed status and the actual body
// bytes passed to the underlying writer. Unwrap and the delegated methods keep
// modern ResponseController operations, streaming, hijacking, HTTP/2 push, and
// io.ReaderFrom optimizations available through the middleware.
type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
	committed    bool
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w}
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseWriter) Committed() bool { return w.committed }

func (w *responseWriter) StatusCode() int {
	if w.committed {
		return w.statusCode
	}
	return http.StatusOK
}

func (w *responseWriter) BytesWritten() int64 { return w.bytesWritten }

func (w *responseWriter) WriteHeader(code int) {
	if w.committed {
		return
	}
	w.committed = true
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(payload []byte) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(payload)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *responseWriter) WriteString(value string) (int, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if writer, ok := w.ResponseWriter.(io.StringWriter); ok {
		n, err := writer.WriteString(value)
		w.bytesWritten += int64(n)
		return n, err
	}
	return w.Write([]byte(value))
}

func (w *responseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(reader)
		w.bytesWritten += n
		return n, err
	}
	// Hide ReadFrom from io.Copy to avoid recursively selecting this method.
	return io.Copy(struct{ io.Writer }{w}, reader)
}

func (w *responseWriter) FlushError() error {
	if !w.committed {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *responseWriter) Flush() { _ = w.FlushError() }

func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	connection, buffered, err := hijacker.Hijack()
	if err == nil && !w.committed {
		w.committed = true
		w.statusCode = http.StatusSwitchingProtocols
	}
	return connection, buffered, err
}

func (w *responseWriter) Push(target string, options *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (w *responseWriter) SetReadDeadline(deadline time.Time) error {
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}

func (w *responseWriter) SetWriteDeadline(deadline time.Time) error {
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}

func (w *responseWriter) EnableFullDuplex() error {
	return http.NewResponseController(w.ResponseWriter).EnableFullDuplex()
}

// setServerTiming adds the Server-Timing header if enabled.
func (s *Server) setServerTiming(w http.ResponseWriter, start time.Time) {
	if s.cfg.EnableServerTiming {
		durationMS := float64(time.Since(start).Microseconds()) / 1000.0
		setCompatibleServerTiming(w, fmt.Sprintf("app;dur=%.3f", durationMS))
	}
}

// setMetaHeaders adds cf-meta-* headers to the response.
func (s *Server) setMetaHeaders(w http.ResponseWriter, clientMeta meta.ClientMeta, requestTime time.Time) {
	w.Header().Set("cf-meta-asn", fmt.Sprintf("%d", clientMeta.ASN))
	w.Header().Set("cf-meta-city", clientMeta.City)
	w.Header().Set("cf-meta-colo", clientMeta.Colo)
	w.Header().Set("cf-meta-country", clientMeta.Country)
	w.Header().Set("cf-meta-ip", clientMeta.ClientIP)
	w.Header().Set("cf-meta-latitude", fmt.Sprintf("%f", clientMeta.Latitude))
	w.Header().Set("cf-meta-longitude", fmt.Sprintf("%f", clientMeta.Longitude))
	w.Header().Set("cf-meta-postalcode", clientMeta.PostalCode)
	w.Header().Set("cf-meta-request-time", fmt.Sprintf("%d", requestTime.UnixMilli()))
	if clientMeta.Timezone != "" {
		w.Header().Set("cf-meta-timezone", clientMeta.Timezone)
	}
}
