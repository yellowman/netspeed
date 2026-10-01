package client

import (
	"encoding/json"
	"testing"
)

func TestResultProviderIdentityMatchesNativeJSONContract(t *testing.T) {
	t.Setenv("NETSPEED_SELECTED_PROVIDER", "netspeed")
	t.Setenv("NETSPEED_MEASUREMENT_CONTRACT", "netspeed-verified-v2")
	t.Setenv("NETSPEED_PACKET_TOPOLOGY", "server-peer")
	encoded, err := json.Marshal(JSONOutput{})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"provider": "netspeed", "measurementContract": "netspeed-verified-v2", "packetTopology": "server-peer"} {
		if result[key] != want {
			t.Fatalf("result %s = %v; want %s", key, result[key], want)
		}
		if _, exists := result["summary"].(map[string]any)[key]; exists {
			t.Fatalf("provider identity leaked into summary: %s", key)
		}
	}
}
