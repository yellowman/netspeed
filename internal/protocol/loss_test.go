package protocol

import (
	"errors"
	"testing"
)

func TestPacketLossFramesAreExactly1200BytesAndRoundTrip(t *testing.T) {
	probe := EncodeProbeFrame(42, 123456)
	if len(probe) != PacketFrameSize {
		t.Fatalf("probe length = %d; want %d", len(probe), PacketFrameSize)
	}
	decoded, err := DecodePacketFrame(probe)
	if err != nil {
		t.Fatalf("DecodePacketFrame(probe): %v", err)
	}
	if decoded.Acknowledgement || decoded.Sequence != 42 || decoded.SentAtUnixMilli != 123456 || decoded.RecvAtUnixMilli != 0 {
		t.Fatalf("decoded probe = %#v", decoded)
	}

	ack := EncodeAckFrame(42, 123456, 123470)
	decoded, err = DecodePacketFrame(ack)
	if err != nil {
		t.Fatalf("DecodePacketFrame(ack): %v", err)
	}
	if !decoded.Acknowledgement || decoded.Sequence != 42 || decoded.RecvAtUnixMilli != 123470 {
		t.Fatalf("decoded ack = %#v", decoded)
	}
}

func TestPacketLossFrameRejectsWrongSizeAndCorruption(t *testing.T) {
	if _, err := DecodePacketFrame(make([]byte, PacketFrameSize-1)); !errors.Is(err, ErrInvalidPacketFrame) {
		t.Fatalf("short frame error = %v; want ErrInvalidPacketFrame", err)
	}

	frame := EncodeProbeFrame(7, 100)
	frame[len(frame)-1] ^= 1
	if _, err := DecodePacketFrame(frame); !errors.Is(err, ErrInvalidPacketFrame) {
		t.Fatalf("corrupt frame error = %v; want ErrInvalidPacketFrame", err)
	}
}
