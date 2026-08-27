package output

import (
	"encoding/json"
	"os"
)

// withProviderIdentity annotates machine-readable native-client results without
// changing the internal measurement model used by human and CSV renderers.
func withProviderIdentity(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) != nil {
		return v
	}
	provider := os.Getenv("NETSPEED_SELECTED_PROVIDER")
	if provider == "" {
		provider = "netspeed"
	}
	contract := os.Getenv("NETSPEED_MEASUREMENT_CONTRACT")
	if contract == "" {
		contract = "netspeed-verified-v2"
	}
	topology := os.Getenv("NETSPEED_PACKET_TOPOLOGY")
	if topology == "" {
		topology = "server-peer"
	}
	obj["provider"] = provider
	obj["measurementContract"] = contract
	obj["packetTopology"] = topology
	return obj
}
