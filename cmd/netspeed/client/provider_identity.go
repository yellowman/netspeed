package client

import (
	"encoding/json"
	"os"
)

func (r Summary) MarshalJSON() ([]byte, error) {
	type raw Summary
	b, err := json.Marshal(raw(r))
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
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
	return json.Marshal(obj)
}
