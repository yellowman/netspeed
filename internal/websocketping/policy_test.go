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
	done := make(chan error, 1)
	go func() {
		done <- serveConnection(server, bufio.NewReader(server), bufio.NewWriter(server), func(bytes int64) error {
			charges = append(charges, bytes)
			if len(charges) > 2 {
				return fmt.Errorf("message budget exhausted")
			}
			return nil
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
	if len(charges) != 3 || charges[0] < 14 || charges[1] < 6 {
		t.Fatalf("control traffic not accounted: %v", charges)
	}
}
