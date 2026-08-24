package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// PacketFrameSize is the exact SCTP user-message size used by the packet
	// loss test. The old protocol merely put "size":1200 in a short JSON body.
	PacketFrameSize = 1200

	// PacketFrameVersion identifies the binary packet-loss frame format.
	PacketFrameVersion = 1

	packetFrameHeaderSize = 32
	packetFrameProbe      = 1
	packetFrameAck        = 2
)

var packetFrameMagic = [4]byte{'N', 'S', 'P', 'L'}

// ErrInvalidPacketFrame means a packet-loss data-channel message did not match
// the exact-size, versioned binary frame contract.
var ErrInvalidPacketFrame = errors.New("invalid packet-loss frame")

// PacketFrame is the decoded header of an exact-size packet-loss message.
type PacketFrame struct {
	Acknowledgement bool
	Sequence        uint32
	SentAtUnixMilli int64
	RecvAtUnixMilli int64
}

// EncodeProbeFrame returns an exact-size probe frame with deterministic padding.
func EncodeProbeFrame(sequence uint32, sentAtUnixMilli int64) []byte {
	return encodePacketFrame(packetFrameProbe, sequence, sentAtUnixMilli, 0)
}

// EncodeAckFrame returns an exact-size acknowledgement frame.
func EncodeAckFrame(sequence uint32, sentAtUnixMilli, recvAtUnixMilli int64) []byte {
	return encodePacketFrame(packetFrameAck, sequence, sentAtUnixMilli, recvAtUnixMilli)
}

func encodePacketFrame(frameType byte, sequence uint32, sentAtUnixMilli, recvAtUnixMilli int64) []byte {
	frame := make([]byte, PacketFrameSize)
	copy(frame[0:4], packetFrameMagic[:])
	frame[4] = PacketFrameVersion
	frame[5] = frameType
	binary.BigEndian.PutUint16(frame[6:8], packetFrameHeaderSize)
	binary.BigEndian.PutUint32(frame[8:12], sequence)
	binary.BigEndian.PutUint64(frame[12:20], uint64(sentAtUnixMilli))
	binary.BigEndian.PutUint64(frame[20:28], uint64(recvAtUnixMilli))
	binary.BigEndian.PutUint32(frame[28:32], uint32(PacketFrameSize))
	fillPacketPadding(frame, sequence)
	return frame
}

func fillPacketPadding(frame []byte, sequence uint32) {
	for index := packetFrameHeaderSize; index < len(frame); index++ {
		frame[index] = byte((uint64(sequence) + uint64(index)*31) & 0xff)
	}
}

// DecodePacketFrame validates and decodes an exact-size packet-loss frame.
func DecodePacketFrame(frame []byte) (PacketFrame, error) {
	if len(frame) != PacketFrameSize {
		return PacketFrame{}, fmt.Errorf("%w: size %d; want %d", ErrInvalidPacketFrame, len(frame), PacketFrameSize)
	}
	if string(frame[0:4]) != string(packetFrameMagic[:]) {
		return PacketFrame{}, fmt.Errorf("%w: bad magic", ErrInvalidPacketFrame)
	}
	if frame[4] != PacketFrameVersion {
		return PacketFrame{}, fmt.Errorf("%w: version %d; want %d", ErrInvalidPacketFrame, frame[4], PacketFrameVersion)
	}
	if binary.BigEndian.Uint16(frame[6:8]) != packetFrameHeaderSize {
		return PacketFrame{}, fmt.Errorf("%w: bad header size", ErrInvalidPacketFrame)
	}
	if binary.BigEndian.Uint32(frame[28:32]) != PacketFrameSize {
		return PacketFrame{}, fmt.Errorf("%w: bad declared frame size", ErrInvalidPacketFrame)
	}

	sequence := binary.BigEndian.Uint32(frame[8:12])
	for index := packetFrameHeaderSize; index < len(frame); index++ {
		want := byte((uint64(sequence) + uint64(index)*31) & 0xff)
		if frame[index] != want {
			return PacketFrame{}, fmt.Errorf("%w: corrupt padding at byte %d", ErrInvalidPacketFrame, index)
		}
	}

	var acknowledgement bool
	switch frame[5] {
	case packetFrameProbe:
		acknowledgement = false
	case packetFrameAck:
		acknowledgement = true
	default:
		return PacketFrame{}, fmt.Errorf("%w: unknown type %d", ErrInvalidPacketFrame, frame[5])
	}

	return PacketFrame{
		Acknowledgement: acknowledgement,
		Sequence:        sequence,
		SentAtUnixMilli: int64(binary.BigEndian.Uint64(frame[12:20])),
		RecvAtUnixMilli: int64(binary.BigEndian.Uint64(frame[20:28])),
	}, nil
}
