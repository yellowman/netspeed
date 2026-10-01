package server

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTURNCredentialAliasesAreIdempotent(t *testing.T) {
	for _, test := range []struct {
		name   string
		urls   []string
		server string
		want   []string
		bare   string
	}{
		{name: "STUN first", urls: []string{"stun:127.0.0.1:3478", "turn:127.0.0.1:3478?transport=udp"}, want: []string{"stun:127.0.0.1:3478", "turn:127.0.0.1:3478?transport=udp"}, bare: "127.0.0.1:3478"},
		{name: "IPv6", urls: []string{"stun:[::1]:3478", "turn:[::1]:3478?transport=udp"}, want: []string{"stun:[::1]:3478", "turn:[::1]:3478?transport=udp"}, bare: "[::1]:3478"},
		{name: "secure TURN", urls: []string{"turns:relay.example:5349?transport=tcp"}, want: []string{"turns:relay.example:5349?transport=tcp"}, bare: "relay.example:5349"},
		{name: "TCP TURN", urls: []string{"turn:relay.example:3478?transport=tcp"}, want: []string{"turn:relay.example:3478?transport=tcp"}, bare: "relay.example:3478"},
		{name: "STUN only", urls: []string{"stun:127.0.0.1:3478"}, want: []string{"stun:127.0.0.1:3478"}},
		{name: "legacy bare server", server: "127.0.0.1:3478", want: []string{"turn:127.0.0.1:3478?transport=udp"}, bare: "127.0.0.1:3478"},
		{name: "legacy secure server", server: "turns:relay.example:5349?transport=tcp", want: []string{"turns:relay.example:5349?transport=tcp"}, bare: "relay.example:5349"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"username": "user", "credential": "secret", "servers": test.urls}
			if test.server != "" {
				payload["server"] = test.server
			}
			first := addCloudflareCredentialAliases(payload)
			if got := first["urls"]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("URLs = %v; want %v", got, test.want)
			}
			if test.bare == "" {
				if _, exists := first["server"]; exists {
					t.Fatal("STUN-only response advertised a TURN server")
				}
			} else if first["server"] != test.bare {
				t.Fatalf("bare server = %v; want %s", first["server"], test.bare)
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate the second pass through the JSON-capturing middleware.
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			second, err := json.Marshal(addCloudflareCredentialAliases(decoded))
			if err != nil {
				t.Fatal(err)
			}
			if string(second) != string(encoded) {
				t.Fatalf("alias wrapping changed response on second pass:\n%s\n%s", encoded, second)
			}
		})
	}
}
