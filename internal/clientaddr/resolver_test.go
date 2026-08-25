package clientaddr

import (
	"net/http/httptest"
	"testing"
)

func TestResolverIgnoresHeadersFromUntrustedPeer(t *testing.T) {
	resolver, err := NewResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.RemoteAddr = "203.0.113.9:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.10")
	request.Header.Set("CF-Connecting-IP", "198.51.100.11")
	if got := resolver.ClientIP(request); got != "203.0.113.9" {
		t.Fatalf("ClientIP=%q; want direct peer", got)
	}
}

func TestResolverWalksForwardedChainFromTrustedEdge(t *testing.T) {
	resolver, err := NewResolver([]string{"10.0.0.0/8", "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.RemoteAddr = "10.0.0.5:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.7, 192.0.2.44")
	if got := resolver.ClientIP(request); got != "198.51.100.7" {
		t.Fatalf("ClientIP=%q; want originating client", got)
	}
}

func TestResolverAcceptsSingleIPHeadersOnlyFromTrustedPeer(t *testing.T) {
	resolver, err := NewResolver([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("CF-Connecting-IP", "2001:db8::9")
	if got := resolver.ClientIP(request); got != "2001:db8::9" {
		t.Fatalf("ClientIP=%q; want CF address", got)
	}
}

func TestResolverRejectsMalformedForwardedChain(t *testing.T) {
	resolver, err := NewResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.RemoteAddr = "10.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "garbage, 198.51.100.5")
	if got := resolver.ClientIP(request); got != "10.0.0.1" {
		t.Fatalf("ClientIP=%q; want direct trusted peer after rejecting malformed chain", got)
	}
}

func TestNewResolverRejectsInvalidCIDR(t *testing.T) {
	if _, err := NewResolver([]string{"not-a-cidr"}); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}
