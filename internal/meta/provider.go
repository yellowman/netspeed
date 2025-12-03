// Package meta provides client metadata extraction and lookup.
package meta

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// ClientMeta holds per-client metadata for the /meta endpoint.
type ClientMeta struct {
	Hostname     string  `json:"hostname"`
	ClientIP     string  `json:"clientIp"`
	HTTPProtocol string  `json:"httpProtocol"`
	ASN          int     `json:"asn"`
	ASOrg        string  `json:"asOrganization"`
	Colo         string  `json:"colo"`
	Country      string  `json:"country"`
	City         string  `json:"city"`
	Region       string  `json:"region"`
	PostalCode   string  `json:"postalCode"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	Timezone     string  `json:"timezone,omitempty"`
}

// Provider is the interface for extracting client metadata from requests.
type Provider interface {
	MetaFor(r *http.Request) ClientMeta
}

// ClientIPFromRequest extracts the client IP from a request.
// If trustProxy is true, it checks X-Forwarded-For and CF-Connecting-IP headers.
func ClientIPFromRequest(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take the first entry before the first comma
			if idx := strings.Index(xff, ","); idx != -1 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
		if cip := r.Header.Get("CF-Connecting-IP"); cip != "" {
			return cip
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// HTTPProtocolFromRequest returns the HTTP protocol version string.
func HTTPProtocolFromRequest(r *http.Request) string {
	return r.Proto
}

// StaticProvider returns fixed values for all requests.
// Useful for testing or single-server deployments.
type StaticProvider struct {
	Hostname   string
	Colo       string
	Country    string
	City       string
	Region     string
	PostalCode string
	Latitude   float64
	Longitude  float64
	Timezone   string
	ASN        int
	ASOrg      string
	TrustProxy bool
}

// MetaFor returns metadata for the given request.
func (p *StaticProvider) MetaFor(r *http.Request) ClientMeta {
	return ClientMeta{
		Hostname:     p.Hostname,
		ClientIP:     ClientIPFromRequest(r, p.TrustProxy),
		HTTPProtocol: HTTPProtocolFromRequest(r),
		ASN:          p.ASN,
		ASOrg:        p.ASOrg,
		Colo:         p.Colo,
		Country:      p.Country,
		City:         p.City,
		Region:       p.Region,
		PostalCode:   p.PostalCode,
		Latitude:     p.Latitude,
		Longitude:    p.Longitude,
		Timezone:     p.Timezone,
	}
}

// HeaderProvider reads metadata from upstream proxy/CDN headers.
// Useful when netspeedd sits behind an existing CDN.
type HeaderProvider struct {
	Hostname   string
	Colo       string
	TrustProxy bool
}

// MetaFor extracts metadata from request headers.
func (p *HeaderProvider) MetaFor(r *http.Request) ClientMeta {
	meta := ClientMeta{
		Hostname:     p.Hostname,
		ClientIP:     ClientIPFromRequest(r, p.TrustProxy),
		HTTPProtocol: HTTPProtocolFromRequest(r),
		Colo:         p.Colo,
	}

	// Read from CF-style headers if present
	if country := r.Header.Get("CF-IPCountry"); country != "" {
		meta.Country = country
	}
	if city := r.Header.Get("CF-City"); city != "" {
		meta.City = city
	}
	if region := r.Header.Get("CF-Region"); region != "" {
		meta.Region = region
	}
	if postalCode := r.Header.Get("CF-Postal-Code"); postalCode != "" {
		meta.PostalCode = postalCode
	}
	if lat := r.Header.Get("CF-Latitude"); lat != "" {
		meta.Latitude = parseFloat(lat)
	}
	if lon := r.Header.Get("CF-Longitude"); lon != "" {
		meta.Longitude = parseFloat(lon)
	}
	if timezone := r.Header.Get("CF-Timezone"); timezone != "" {
		meta.Timezone = timezone
	}

	return meta
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
