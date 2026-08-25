// Package clientaddr resolves a request's effective client address without
// trusting forwarding headers from arbitrary Internet peers.
package clientaddr

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver trusts forwarding headers only when the request's direct peer is in
// one of the configured proxy prefixes.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver parses trusted proxy CIDRs. An empty list creates a direct-peer
// resolver that ignores all forwarding headers.
func NewResolver(cidrs []string) (*Resolver, error) {
	resolver := &Resolver{}
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("parse trusted proxy CIDR %q: %w", raw, err)
		}
		resolver.trusted = append(resolver.trusted, prefix.Masked())
	}
	return resolver, nil
}

// DirectIP returns the validated direct TCP peer address. Invalid RemoteAddr
// values are returned without a port so logging still has a stable identity.
func DirectIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		if address, parseErr := netip.ParseAddr(host); parseErr == nil {
			return address.Unmap().String()
		}
		return host
	}
	if address, parseErr := netip.ParseAddr(request.RemoteAddr); parseErr == nil {
		return address.Unmap().String()
	}
	return request.RemoteAddr
}

// ClientIP returns the effective client IP. X-Forwarded-For is evaluated from
// the trusted edge inward; the first untrusted address is the client. Single-IP
// headers are accepted only from a trusted direct peer.
func (resolver *Resolver) ClientIP(request *http.Request) string {
	directString := DirectIP(request)
	direct, err := netip.ParseAddr(directString)
	if err != nil || !resolver.isTrusted(direct) {
		return directString
	}
	direct = direct.Unmap()

	if forwarded := resolver.forwardedForClient(request.Header.Values("X-Forwarded-For"), direct); forwarded != "" {
		return forwarded
	}
	if connecting := validHeaderIP(request.Header.Get("CF-Connecting-IP")); connecting != "" {
		return connecting
	}
	if realIP := validHeaderIP(request.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return direct.String()
}

func (resolver *Resolver) forwardedForClient(values []string, direct netip.Addr) string {
	var chain []netip.Addr
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				return ""
			}
			address, err := netip.ParseAddr(item)
			if err != nil {
				// A partially malformed chain is ambiguous. Ignore the complete
				// header rather than accepting an attacker-selected surviving hop.
				return ""
			}
			chain = append(chain, address.Unmap())
		}
	}
	if len(chain) == 0 {
		return ""
	}

	current := direct
	for index := len(chain) - 1; index >= 0; index-- {
		if !resolver.isTrusted(current) {
			return current.String()
		}
		current = chain[index]
	}
	return current.String()
}

func validHeaderIP(value string) string {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return address.Unmap().String()
}

func (resolver *Resolver) isTrusted(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range resolver.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
