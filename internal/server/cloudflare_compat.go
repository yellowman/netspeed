package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// addCloudflareCredentialAliases makes the credential response consumable by
// Netspeed clients, Cloudflare's speed-test SDK, and the Realtime TURN API shape.
func addCloudflareCredentialAliases(payload map[string]any) map[string]any {
	username, _ := payload["username"].(string)
	credential, _ := payload["credential"].(string)
	var urls []string
	for _, key := range []string{"urls", "servers"} {
		switch v := payload[key].(type) {
		case []string:
			urls = append(urls, v...)
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					urls = append(urls, s)
				}
			}
		}
	}
	if s, ok := payload["server"].(string); ok && s != "" {
		full := s
		if !strings.HasPrefix(strings.ToLower(full), "turn:") {
			full = "turn:" + full
		}
		if !strings.Contains(strings.ToLower(full), "transport=") {
			full += "?transport=udp"
		}
		urls = append(urls, full)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(urls))
	for _, s := range urls {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		payload["urls"] = out
		payload["servers"] = out
		payload["iceServers"] = map[string]any{"urls": out, "username": username, "credential": credential}
		bare := strings.TrimPrefix(out[0], "turn:")
		bare = strings.TrimPrefix(bare, "turns:")
		if i := strings.IndexByte(bare, '?'); i >= 0 {
			bare = bare[:i]
		}
		if at := strings.LastIndexByte(bare, '@'); at >= 0 {
			bare = bare[at+1:]
		}
		payload["server"] = bare
	}
	return payload
}

func writeTURNCompatibilityJSON(w http.ResponseWriter, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) == nil {
		payload = addCloudflareCredentialAliases(obj)
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(payload)
}

func setCompatibleServerTiming(w http.ResponseWriter, value string) {
	if !strings.Contains(strings.ToLower(value), "cfspeed") {
		for _, entry := range strings.Split(value, ",") {
			entry = strings.TrimSpace(entry)
			if !strings.HasPrefix(strings.ToLower(entry), "app;") {
				continue
			}
			if i := strings.Index(strings.ToLower(entry), "dur="); i >= 0 {
				dur := entry[i+4:]
				if j := strings.IndexAny(dur, "; "); j >= 0 {
					dur = dur[:j]
				}
				value += ", cfSpeedApp;dur=" + dur
				break
			}
		}
	}
	w.Header().Set("Server-Timing", value)
}

type compatibilityCaptureWriter struct {
	header http.Header
	status int
	body   strings.Builder
}

func (w *compatibilityCaptureWriter) Header() http.Header { return w.header }
func (w *compatibilityCaptureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *compatibilityCaptureWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func turnCompatibilityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cw := &compatibilityCaptureWriter{header: make(http.Header)}
		next.ServeHTTP(cw, r)
		for k, values := range cw.header {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		status := cw.status
		if status == 0 {
			status = http.StatusOK
		}
		body := []byte(cw.body.String())
		if status >= 200 && status < 300 {
			var obj map[string]any
			if json.Unmarshal(body, &obj) == nil {
				if encoded, err := json.Marshal(addCloudflareCredentialAliases(obj)); err == nil {
					body = append(encoded, '\n')
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				}
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}
