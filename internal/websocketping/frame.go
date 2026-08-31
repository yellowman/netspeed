// Package websocketping implements the small RFC 6455 subset used by the
// Netspeed application-level latency echo protocol. It deliberately uses only
// the Go standard library so the daemon and CLI do not gain another transport
// dependency.
package websocketping

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa

	closeNormal            = 1000
	closeProtocolError     = 1002
	closeUnsupportedData   = 1003
	closeInvalidPayload    = 1007
	maximumControlPayload  = 125
	maximumAcceptedPayload = 1024
)

type frame struct {
	opcode  byte
	payload []byte
}

func readFrame(reader io.Reader, expectMasked bool) (frame, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return frame{}, err
	}
	if header[0]&0x70 != 0 {
		return frame{}, errors.New("WebSocket frame used unsupported RSV bits")
	}
	if header[0]&0x80 == 0 {
		return frame{}, errors.New("fragmented WebSocket frames are not supported")
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	if masked != expectMasked {
		if expectMasked {
			return frame{}, errors.New("client WebSocket frame was not masked")
		}
		return frame{}, errors.New("server WebSocket frame was unexpectedly masked")
	}

	payloadLength := uint64(header[1] & 0x7f)
	switch payloadLength {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return frame{}, err
		}
		payloadLength = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return frame{}, err
		}
		payloadLength = binary.BigEndian.Uint64(extended[:])
		if payloadLength&(uint64(1)<<63) != 0 {
			return frame{}, errors.New("invalid WebSocket payload length")
		}
	}
	if opcode >= opClose && payloadLength > maximumControlPayload {
		return frame{}, errors.New("oversized WebSocket control frame")
	}
	if payloadLength > maximumAcceptedPayload {
		return frame{}, fmt.Errorf("WebSocket payload length %d exceeds %d", payloadLength, maximumAcceptedPayload)
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return frame{}, err
		}
	}
	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return frame{}, err
	}
	if masked {
		for index := range payload {
			payload[index] ^= mask[index%len(mask)]
		}
	}
	return frame{opcode: opcode, payload: payload}, nil
}

func writeFrame(writer io.Writer, opcode byte, payload []byte, masked bool) error {
	if len(payload) > maximumAcceptedPayload {
		return fmt.Errorf("WebSocket payload length %d exceeds %d", len(payload), maximumAcceptedPayload)
	}
	if opcode >= opClose && len(payload) > maximumControlPayload {
		return errors.New("oversized WebSocket control frame")
	}

	header := make([]byte, 0, 14)
	header = append(header, 0x80|(opcode&0x0f))
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case len(payload) <= 125:
		header = append(header, maskBit|byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, maskBit|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, maskBit|127)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(len(payload)))
		header = append(header, extended[:]...)
	}

	if !masked {
		if err := writeAll(writer, header); err != nil {
			return err
		}
		return writeAll(writer, payload)
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("generate WebSocket mask: %w", err)
	}
	header = append(header, mask[:]...)
	if err := writeAll(writer, header); err != nil {
		return err
	}
	maskedPayload := make([]byte, len(payload))
	for index, value := range payload {
		maskedPayload[index] = value ^ mask[index%len(mask)]
	}
	return writeAll(writer, maskedPayload)
}

func writeClose(writer io.Writer, code uint16, reason string, masked bool) error {
	payload := make([]byte, 2, maximumControlPayload)
	binary.BigEndian.PutUint16(payload, code)
	remaining := maximumControlPayload - len(payload)
	if len(reason) > remaining {
		reason = reason[:remaining]
	}
	payload = append(payload, reason...)
	return writeFrame(writer, opClose, payload, masked)
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}
