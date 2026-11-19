// Package protocol defines the binary wire format for jitterlab audio
// frames and validates all incoming data as untrusted input.
//
// Header layout (big-endian, 21 bytes):
//
//	0  magic uint16 (0x4A4C "JL")
//	2  stream id uint32
//	6  sequence number uint32 (wraps mod 2^32)
//	10 send timestamp int64, milliseconds on the sender clock
//	18 frame type uint8
//	19 payload length uint16
//	21 payload bytes
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	Magic      uint16 = 0x4A4C
	HeaderSize        = 21
	// MaxPayload is the absolute payload ceiling; callers may impose lower.
	MaxPayload = 4096
)

// FrameType identifies the kind of a frame.
type FrameType uint8

const (
	FrameAudio         FrameType = 0
	FrameComfortNoise  FrameType = 1
	FrameStreamMarker  FrameType = 2
)

func (t FrameType) valid() bool { return t <= FrameStreamMarker }

var (
	ErrTruncated   = errors.New("protocol: truncated packet")
	ErrBadMagic    = errors.New("protocol: bad magic")
	ErrBadLength   = errors.New("protocol: payload length mismatch")
	ErrUnknownType = errors.New("protocol: unknown frame type")
	ErrTooLarge    = errors.New("protocol: payload exceeds limit")
)

// Packet is a decoded frame. Payload is owned by the Packet.
type Packet struct {
	StreamID   uint32
	Seq        uint32
	SendTimeMs int64
	Type       FrameType
	Payload    []byte
}

// Encode serializes p. Payloads longer than MaxPayload are truncated by the
// uint16 length field, so callers must keep payloads within limits.
func Encode(p Packet) []byte {
	buf := make([]byte, HeaderSize+len(p.Payload))
	binary.BigEndian.PutUint16(buf[0:2], Magic)
	binary.BigEndian.PutUint32(buf[2:6], p.StreamID)
	binary.BigEndian.PutUint32(buf[6:10], p.Seq)
	binary.BigEndian.PutUint64(buf[10:18], uint64(p.SendTimeMs))
	buf[18] = byte(p.Type)
	binary.BigEndian.PutUint16(buf[19:21], uint16(len(p.Payload)))
	copy(buf[HeaderSize:], p.Payload)
	return buf
}

// Decode parses buf, rejecting truncated, malformed, unknown-type and
// oversize packets. maxPayload <= 0 falls back to MaxPayload. The returned
// payload is a copy, so buf may be reused by the caller.
func Decode(buf []byte, maxPayload int) (Packet, error) {
	var p Packet
	if maxPayload <= 0 || maxPayload > MaxPayload {
		maxPayload = MaxPayload
	}
	if len(buf) < HeaderSize {
		return p, ErrTruncated
	}
	if binary.BigEndian.Uint16(buf[0:2]) != Magic {
		return p, ErrBadMagic
	}
	typ := FrameType(buf[18])
	if !typ.valid() {
		return p, fmt.Errorf("%w: %d", ErrUnknownType, typ)
	}
	plen := int(binary.BigEndian.Uint16(buf[19:21]))
	if plen > maxPayload {
		return p, fmt.Errorf("%w: %d > %d", ErrTooLarge, plen, maxPayload)
	}
	if len(buf) != HeaderSize+plen {
		return p, fmt.Errorf("%w: header says %d, datagram carries %d", ErrBadLength, plen, len(buf)-HeaderSize)
	}
	p = Packet{
		StreamID:   binary.BigEndian.Uint32(buf[2:6]),
		Seq:        binary.BigEndian.Uint32(buf[6:10]),
		SendTimeMs: int64(binary.BigEndian.Uint64(buf[10:18])),
		Type:       typ,
		Payload:    append([]byte(nil), buf[HeaderSize:]...),
	}
	return p, nil
}
