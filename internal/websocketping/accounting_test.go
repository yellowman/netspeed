package websocketping

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yellowman/netspeed/internal/limits"
)

type observedConnection struct {
	net.Conn
	read, written atomic.Int64
}

func (connection *observedConnection) Read(buffer []byte) (int, error) {
	n, err := connection.Conn.Read(buffer)
	connection.read.Add(int64(n))
	return n, err
}

func (connection *observedConnection) Write(buffer []byte) (int, error) {
	n, err := connection.Conn.Write(buffer)
	connection.written.Add(int64(n))
	return n, err
}

func quotaPolicy(quota *limits.ByteQuota) Policy {
	return Policy{
		OnRead: func(n int64) error {
			if !quota.Charge("client", n).Allowed {
				return errors.New("quota exhausted")
			}
			return nil
		},
		ReserveWrite: func(n int64) (func(int64), error) {
			result, settle := quota.ReserveWrite("client", n)
			if !result.Allowed {
				return nil, errors.New("quota exhausted")
			}
			return settle, nil
		},
		OnPolicyClose: func(n int64) { quota.Charge("client", n) },
	}
}

func TestFrameQuotaEqualsActualWireBytes(t *testing.T) {
	valid, err := NewPayload(1)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		opcode   byte
		payload  []byte
		reply    byte
		code     uint16
		prefetch bool
	}{
		{"binary echo", opBinary, valid, opBinary, 0, false},
		{"prefetched binary echo", opBinary, valid, opBinary, 0, true},
		{"pong without response", opPong, []byte("unsolicited pong"), 0, 0, false},
		{"normal close", opClose, []byte{3, 232, 'b', 'y', 'e'}, opClose, closeNormal, false},
		{"invalid binary", opBinary, []byte("broken"), opClose, closeInvalidPayload, false},
		{"extended binary header", opBinary, make([]byte, 128), opClose, closeInvalidPayload, false},
		{"control ping", opPing, []byte("control"), opPong, 0, false},
		{"text", opText, []byte("text"), opClose, closeUnsupportedData, false},
		{"unsupported opcode", 3, []byte("unknown"), opClose, closeProtocolError, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, client := net.Pipe()
			t.Cleanup(func() { client.Close(); server.Close() })
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			observed := &observedConnection{Conn: server}
			quota := limits.NewByteQuota(10_000, time.Minute)
			done := make(chan error, 1)
			go func() {
				reader := bufio.NewReader(observed)
				if test.prefetch {
					if _, err := reader.Peek(6 + len(test.payload)); err != nil {
						done <- err
						return
					}
				}
				done <- serveConnection(observed, reader, quotaPolicy(quota))
			}()
			if err := writeFrame(client, test.opcode, test.payload, true); err != nil {
				t.Fatal(err)
			}
			if test.reply != 0 {
				response, err := readFrame(client, false)
				if err != nil || response.opcode != test.reply {
					t.Fatalf("reply=%+v error=%v", response, err)
				}
				if test.code != 0 && (len(response.payload) < 2 || binary.BigEndian.Uint16(response.payload) != test.code) {
					t.Fatalf("close payload=%v; want code %d", response.payload, test.code)
				}
			}
			_ = client.Close()
			<-done
			wireBytes := observed.read.Load() + observed.written.Load()
			if got := quota.Used("client"); got != wireBytes {
				t.Fatalf("quota charge=%d; actual inbound=%d outbound=%d", got, observed.read.Load(), observed.written.Load())
			}
			if test.reply == 0 && observed.written.Load() != 0 {
				t.Fatal("pong unexpectedly emitted server traffic")
			}
		})
	}
}

func TestFrameQuotaChargesBytesBeyondRejectedHeader(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	observed := &observedConnection{Conn: server}
	quota := limits.NewByteQuota(100, time.Minute)
	done := make(chan error, 1)
	go func() {
		done <- serveConnection(observed, bufio.NewReader(observed), quotaPolicy(quota))
	}()
	// Unsupported RSV bits reject the frame after two bytes, but buffering has
	// already received this entire header, including its four-byte mask.
	header := []byte{0xc2, 0x90, 1, 2, 3, 4}
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("invalid header was accepted")
	}
	if quota.Used("client") != int64(len(header)) || observed.read.Load() != int64(len(header)) {
		t.Fatalf("rejected header charge=%d actual read=%d", quota.Used("client"), observed.read.Load())
	}
}

func TestFrameQuotaChargesIncompleteFrame(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	observed := &observedConnection{Conn: server}
	quota := limits.NewByteQuota(100, time.Minute)
	done := make(chan error, 1)
	go func() {
		done <- serveConnection(observed, bufio.NewReader(observed), quotaPolicy(quota))
	}()
	// Advertise a sixteen-byte payload, deliver only three bytes, then abort.
	partial := []byte{0x82, 0x90, 1, 2, 3, 4, 5, 6, 7}
	if _, err := client.Write(partial); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if err := <-done; err == nil {
		t.Fatal("incomplete frame was accepted")
	}
	if got := quota.Used("client"); got != int64(len(partial)) || observed.written.Load() != 0 {
		t.Fatalf("incomplete frame charge=%d outbound=%d", got, observed.written.Load())
	}
}

type partialWriteConnection struct {
	net.Conn
	write func([]byte) (int, error)
}

func (connection partialWriteConnection) Write(buffer []byte) (int, error) {
	return connection.write(buffer)
}

func TestFrameQuotaSettlesPartialAndFailedWrites(t *testing.T) {
	for _, actual := range []int{0, 5} {
		quota := limits.NewByteQuota(100, time.Minute)
		connection := partialWriteConnection{write: func(buffer []byte) (int, error) {
			if quota.Used("client") != int64(len(buffer)) {
				t.Fatal("encoded frame was not reserved before the write")
			}
			return actual, io.ErrClosedPipe
		}}
		err := sendFrame(connection, opBinary, make([]byte, 16), quotaPolicy(quota))
		if !errors.Is(err, io.ErrClosedPipe) || quota.Used("client") != int64(actual) {
			t.Fatalf("actual=%d charge=%d error=%v", actual, quota.Used("client"), err)
		}
	}
}

func TestPolicyCloseChargesOnlyActualWrittenBytes(t *testing.T) {
	for _, actual := range []int{0, 5} {
		quota := limits.NewByteQuota(100, time.Minute)
		remaining := actual
		connection := partialWriteConnection{write: func(buffer []byte) (int, error) {
			if len(buffer) <= remaining {
				remaining -= len(buffer)
				return len(buffer), nil
			}
			n := remaining
			remaining = 0
			return n, io.ErrClosedPipe
		}}
		policyClose(connection, errors.New("quota exhausted"), quotaPolicy(quota))
		if got := quota.Used("client"); got != int64(actual) {
			t.Fatalf("closing notification charge=%d; want %d", got, actual)
		}
	}
}
