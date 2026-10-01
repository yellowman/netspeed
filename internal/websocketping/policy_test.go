package websocketping

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestPolicyLimitsControlPingAndPongFrames(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	var charges []int64
	frames := 0
	done := make(chan error, 1)
	go func() {
		done <- serveConnection(server, bufio.NewReader(server), Policy{
			OnRead: func(bytes int64) error { charges = append(charges, bytes); return nil },
			OnFrame: func() error {
				frames++
				if frames > 2 {
					return fmt.Errorf("message budget exhausted")
				}
				return nil
			},
		})
	}()
	reader := bufio.NewReader(client)
	if err := writeFrame(client, opPing, []byte("control"), true); err != nil {
		t.Fatal(err)
	}
	response, err := readFrame(reader, false)
	if err != nil || response.opcode != opPong {
		t.Fatalf("control ping: %+v %v", response, err)
	}
	if err := writeFrame(client, opPong, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(client, opPing, nil, true); err != nil {
		t.Fatal(err)
	}
	response, err = readFrame(reader, false)
	if err != nil || response.opcode != opClose || binary.BigEndian.Uint16(response.payload[:2]) != 1008 {
		t.Fatalf("policy close: %+v %v", response, err)
	}
	if err := <-done; err == nil {
		t.Fatal("unbounded control traffic")
	}
	var bytes int64
	for _, charge := range charges {
		bytes += charge
	}
	// Network reads may split a frame's header and payload. The message rate
	// counts frames, while byte accounting must total only the received bytes.
	if frames != 3 || bytes != 13+6+6 {
		t.Fatalf("control traffic not accounted: frames=%d charges=%v", frames, charges)
	}
}
