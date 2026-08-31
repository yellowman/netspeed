package websocketping

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"

	"github.com/yellowman/netspeed/internal/measurementhttp"
)

var payloadMagic = [4]byte{'N', 'S', 'P', '1'}

// NewPayload creates one fixed-size application ping. The sequence field helps
// diagnostics while the random suffix prevents a stale or duplicated echo from
// being accepted as the current response.
func NewPayload(sequence uint32) ([]byte, error) {
	payload := make([]byte, measurementhttp.WebSocketPingPayloadBytes)
	copy(payload, payloadMagic[:])
	binary.BigEndian.PutUint32(payload[4:8], sequence)
	if _, err := rand.Read(payload[8:]); err != nil {
		return nil, fmt.Errorf("generate WebSocket ping nonce: %w", err)
	}
	return payload, nil
}

// ValidatePayload enforces the advertised fixed-size binary echo contract.
func ValidatePayload(payload []byte) error {
	if len(payload) != measurementhttp.WebSocketPingPayloadBytes {
		return fmt.Errorf("WebSocket ping payload has %d bytes; expected %d", len(payload), measurementhttp.WebSocketPingPayloadBytes)
	}
	for index, expected := range payloadMagic {
		if payload[index] != expected {
			return fmt.Errorf("WebSocket ping payload has invalid magic")
		}
	}
	return nil
}
