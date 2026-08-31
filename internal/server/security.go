package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yellowman/netspeed/internal/limits"
	"github.com/yellowman/netspeed/internal/measurementhttp"
)

var errBandwidthQuotaExceeded = errors.New("client bandwidth quota exceeded")

type bandwidthQuotaError struct {
	retryAfter time.Duration
}

func (err *bandwidthQuotaError) Error() string {
	return errBandwidthQuotaExceeded.Error()
}

func (err *bandwidthQuotaError) Unwrap() error {
	return errBandwidthQuotaExceeded
}

type quotaChargingReader struct {
	reader io.Reader
	quota  *limits.ByteQuota
	key    string
}

func (reader *quotaChargingReader) Read(buffer []byte) (int, error) {
	n, readErr := reader.reader.Read(buffer)
	if n > 0 {
		result := reader.quota.Charge(reader.key, int64(n))
		if !result.Allowed {
			return n, &bandwidthQuotaError{retryAfter: result.RetryAfter}
		}
	}
	return n, readErr
}

func (s *Server) clientIP(request *http.Request) string {
	return s.clientAddress.ClientIP(request)
}

func (s *Server) authenticationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requiredToken := ""
		switch {
		case r.URL.Path == "/health":
			next.ServeHTTP(w, r)
			return
		case r.URL.Path == "/metrics":
			requiredToken = s.cfg.MetricsToken
			if requiredToken == "" {
				requiredToken = s.cfg.AccessToken
			}
		case isProtectedServicePath(r.URL.Path):
			requiredToken = s.cfg.AccessToken
		default:
			next.ServeHTTP(w, r)
			return
		}

		if requiredToken == "" || validBearerToken(r.Header.Get("Authorization"), requiredToken) {
			next.ServeHTTP(w, r)
			return
		}

		s.metrics.authRejected.Add(1)
		w.Header().Set("WWW-Authenticate", `Bearer realm="netspeed"`)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func isProtectedServicePath(path string) bool {
	switch path {
	case "/meta", "/__down", "/__up", "/__ping", "/__ws", "/locations", "/cdn-cgi/trace":
		return true
	default:
		return strings.HasPrefix(path, "/api/")
	}
}

func validBearerToken(header, expected string) bool {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	provided := []byte(fields[1])
	wanted := []byte(expected)
	if len(provided) != len(wanted) {
		return false
	}
	return subtle.ConstantTimeCompare(provided, wanted) == 1
}

func (s *Server) beginTransfer(w http.ResponseWriter, request *http.Request) (func(), bool) {
	clientKey := s.clientIP(request)
	releaseLimit, rejection := s.transferLimiter.Acquire(clientKey)
	if rejection != limits.TransferAdmitted {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Cache-Control", measurementhttp.CacheControl)
		switch rejection {
		case limits.TransferRejectedClient:
			s.metrics.transferRejectedClient.Add(1)
			http.Error(w, "too many active transfers for this client", http.StatusTooManyRequests)
		default:
			s.metrics.transferRejectedGlobal.Add(1)
			http.Error(w, "measurement service is at transfer capacity", http.StatusServiceUnavailable)
		}
		return nil, false
	}

	s.metrics.activeTransfers.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseLimit()
			s.metrics.activeTransfers.Add(-1)
		})
	}, true
}

func (s *Server) reserveBandwidth(w http.ResponseWriter, clientKey string, bytes int64) bool {
	result := s.bandwidthQuota.Reserve(clientKey, bytes)
	if result.Allowed {
		if s.cfg.ClientBandwidthQuotaBytes > 0 {
			w.Header().Set("X-Netspeed-Quota-Remaining-Bytes", strconv.FormatInt(result.Remaining, 10))
		}
		return true
	}

	s.metrics.bandwidthQuotaRejected.Add(1)
	setRetryAfter(w, result.RetryAfter)
	w.Header().Set("Cache-Control", measurementhttp.CacheControl)
	http.Error(w, errBandwidthQuotaExceeded.Error(), http.StatusTooManyRequests)
	return false
}

func setRetryAfter(w http.ResponseWriter, duration time.Duration) {
	seconds := int64(duration/time.Second) + 1
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

func rejectRateLimited(w http.ResponseWriter, retryAfter time.Duration, message string) {
	setRetryAfter(w, retryAfter)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, message, http.StatusTooManyRequests)
}

func (s *Server) decodeControlJSON(w http.ResponseWriter, request *http.Request, maxBytes int64, destination any) bool {
	if request.ContentLength > maxBytes {
		s.metrics.controlBodiesRejected.Add(1)
		http.Error(w, fmt.Sprintf("request body exceeds %d bytes", maxBytes), http.StatusRequestEntityTooLarge)
		return false
	}
	contentType := request.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		s.metrics.controlBodiesRejected.Add(1)
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}

	request.Body = http.MaxBytesReader(w, request.Body, maxBytes)
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(destination); err != nil {
		s.metrics.controlBodiesRejected.Add(1)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("request body exceeds %d bytes", maxBytes), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid request body", http.StatusBadRequest)
		}
		return false
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		s.metrics.controlBodiesRejected.Add(1)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("request body exceeds %d bytes", maxBytes), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "request body must contain one JSON value", http.StatusBadRequest)
		}
		return false
	}
	return true
}
