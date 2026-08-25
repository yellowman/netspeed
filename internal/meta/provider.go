// Package meta provides client metadata extraction and lookup.
package meta

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/yellowman/netspeed/internal/clientaddr"
)

// ClientMeta holds per-client metadata for the /meta endpoint.
type ClientMeta struct {
	Hostname                        string  `json:"hostname"`
	ClientIP                        string  `json:"clientIp"`
	HTTPProtocol                    string  `json:"httpProtocol"`
	ASN                             int     `json:"asn"`
	ASOrg                           string  `json:"asOrganization"`
	Colo                            string  `json:"colo"`
	Country                         string  `json:"country"`
	City                            string  `json:"city"`
	Region                          string  `json:"region"`
	PostalCode                      string  `json:"postalCode"`
	Latitude                        float64 `json:"latitude"`
	Longitude                       float64 `json:"longitude"`
	Timezone                        string  `json:"timezone,omitempty"`
	MaxTransferBytes                int64   `json:"maxTransferBytes"`
	MaxConcurrentTransfersPerClient int     `json:"maxConcurrentTransfersPerClient"`
	MeasurementProtocolVersion      int     `json:"measurementProtocolVersion"`
	UploadReceiptVersion            int     `json:"uploadReceiptVersion"`
	PacketLossFrameVersion          int     `json:"packetLossFrameVersion"`
}

// Provider is the interface for extracting client metadata from requests.
type Provider interface {
	MetaFor(r *http.Request) ClientMeta
}

// ClientIPFromRequest resolves the request identity using only forwarding
// headers supplied by configured trusted proxy CIDRs. A nil resolver always
// returns the direct TCP peer.
func ClientIPFromRequest(request *http.Request, resolver *clientaddr.Resolver) string {
	if resolver == nil {
		return clientaddr.DirectIP(request)
	}
	return resolver.ClientIP(request)
}

// HTTPProtocolFromRequest returns the HTTP protocol version string.
func HTTPProtocolFromRequest(r *http.Request) string {
	return r.Proto
}

// StaticProvider returns fixed values for all requests.
// Useful for testing or single-server deployments.
type StaticProvider struct {
	Hostname      string
	Colo          string
	Country       string
	City          string
	Region        string
	PostalCode    string
	Latitude      float64
	Longitude     float64
	Timezone      string
	ASN           int
	ASOrg         string
	ClientAddress *clientaddr.Resolver
}

// MetaFor returns metadata for the given request.
func (p *StaticProvider) MetaFor(r *http.Request) ClientMeta {
	return ClientMeta{
		Hostname:     p.Hostname,
		ClientIP:     ClientIPFromRequest(r, p.ClientAddress),
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

// HeaderProvider reads metadata from upstream proxy/CDN headers. Deployments
// should use it only behind the same trusted proxy boundary as ClientAddress.
type HeaderProvider struct {
	Hostname      string
	Colo          string
	ClientAddress *clientaddr.Resolver
}

// MetaFor extracts metadata from request headers.
func (p *HeaderProvider) MetaFor(r *http.Request) ClientMeta {
	metadata := ClientMeta{
		Hostname:     p.Hostname,
		ClientIP:     ClientIPFromRequest(r, p.ClientAddress),
		HTTPProtocol: HTTPProtocolFromRequest(r),
		Colo:         p.Colo,
	}

	if country := r.Header.Get("CF-IPCountry"); country != "" {
		metadata.Country = country
	}
	if city := r.Header.Get("CF-City"); city != "" {
		metadata.City = city
	}
	if region := r.Header.Get("CF-Region"); region != "" {
		metadata.Region = region
	}
	if postalCode := r.Header.Get("CF-Postal-Code"); postalCode != "" {
		metadata.PostalCode = postalCode
	}
	if lat := r.Header.Get("CF-Latitude"); lat != "" {
		metadata.Latitude = parseFloat(lat)
	}
	if lon := r.Header.Get("CF-Longitude"); lon != "" {
		metadata.Longitude = parseFloat(lon)
	}
	if timezone := r.Header.Get("CF-Timezone"); timezone != "" {
		metadata.Timezone = timezone
	}

	return metadata
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
