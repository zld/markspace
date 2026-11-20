// Package packet defines the wire format used by both receive paths and the
// validation rules that keep malformed input out of the jitter buffer.
package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// Version is the only wire version this build understands.
	Version byte = 1
	// HeaderLen is the fixed header size in bytes.
	HeaderLen = 16
	// MaxFrames caps the number of media frames carried by one packet.
	MaxFrames = 8
	// MaxPayload caps the payload carried by one packet.
	MaxPayload = 1200
	// MaxPacketLen is the largest packet accepted from the wire.
	MaxPacketLen = HeaderLen + MaxPayload
)

// ErrInvalidPacket is returned by Unmarshal for every malformed input. The
// concrete validation failure is wrapped so callers can log it while counting
// a single invalid-packet bucket.
var ErrInvalidPacket = errors.New("invalid packet")

// Header is the fixed part of the wire format.
//
//	offset  size  field
//	0       1     version
//	1       1     flags (bit0 = end of frame)
//	2       2     stream id (big endian)
//	4       4     seq (big endian, wraps at 2^32)
//	8       4     capture time in milliseconds (big endian, wraps at 2^32)
//	12      1     frame count
//	13      1     payload length
//	14      2     CRC-16/CCITT over header[0:14] and the payload
type Header struct {
	Version   byte
	Flags     byte
	StreamID  uint16
	Seq       uint32
	Timestamp uint32
	Frames    byte
	Payload   int
}

// Packet is one validated wire packet.
type Packet struct {
	Header  Header
	Payload []byte
}

// New builds a packet with the current wire version.
func New(streamID uint16, seq, timestamp uint32, frames int, endOfFrame bool, payload []byte) (*Packet, error) {
	if frames < 1 || frames > MaxFrames {
		return nil, fmt.Errorf("%w: frame count %d outside [1,%d]", ErrInvalidPacket, frames, MaxFrames)
	}
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("%w: payload %d exceeds %d", ErrInvalidPacket, len(payload), MaxPayload)
	}
	var flags byte
	if endOfFrame {
		flags = 1
	}
	return &Packet{
		Header: Header{
			Version:   Version,
			Flags:     flags,
			StreamID:  streamID,
			Seq:       seq,
			Timestamp: timestamp,
			Frames:    byte(frames),
			Payload:   len(payload),
		},
		Payload: append([]byte(nil), payload...),
	}, nil
}

// EndOfFrame reports whether the packet closes a frame.
func (p *Packet) EndOfFrame() bool { return p.Header.Flags&1 != 0 }

// Marshal encodes a packet including its checksum.
func Marshal(p *Packet) ([]byte, error) {
	if p.Header.Version != Version {
		return nil, fmt.Errorf("%w: unknown version %d", ErrInvalidPacket, p.Header.Version)
	}
	if p.Header.Frames < 1 || p.Header.Frames > MaxFrames {
		return nil, fmt.Errorf("%w: frame count %d outside [1,%d]", ErrInvalidPacket, p.Header.Frames, MaxFrames)
	}
	if p.Header.Payload != len(p.Payload) || p.Header.Payload > MaxPayload {
		return nil, fmt.Errorf("%w: payload length mismatch", ErrInvalidPacket)
	}
	buf := make([]byte, HeaderLen+len(p.Payload))
	buf[0] = p.Header.Version
	buf[1] = p.Header.Flags
	binary.BigEndian.PutUint16(buf[2:4], p.Header.StreamID)
	binary.BigEndian.PutUint32(buf[4:8], p.Header.Seq)
	binary.BigEndian.PutUint32(buf[8:12], p.Header.Timestamp)
	buf[12] = p.Header.Frames
	buf[13] = byte(p.Header.Payload)
	sum := Checksum(buf[:14], p.Payload)
	binary.BigEndian.PutUint16(buf[14:16], sum)
	copy(buf[HeaderLen:], p.Payload)
	return buf, nil
}

// Unmarshal validates and decodes a packet. A returned error always wraps
// ErrInvalidPacket and no partially decoded value is returned.
func Unmarshal(buf []byte) (*Packet, error) {
	if len(buf) < HeaderLen {
		return nil, fmt.Errorf("%w: truncated header, %d bytes", ErrInvalidPacket, len(buf))
	}
	if len(buf) > MaxPacketLen {
		return nil, fmt.Errorf("%w: oversized packet, %d bytes", ErrInvalidPacket, len(buf))
	}
	h := Header{
		Version:   buf[0],
		Flags:     buf[1],
		StreamID:  binary.BigEndian.Uint16(buf[2:4]),
		Seq:       binary.BigEndian.Uint32(buf[4:8]),
		Timestamp: binary.BigEndian.Uint32(buf[8:12]),
		Frames:    buf[12],
		Payload:   int(buf[13]),
	}
	if h.Version != Version {
		return nil, fmt.Errorf("%w: unknown version %d", ErrInvalidPacket, h.Version)
	}
	if h.Frames < 1 || h.Frames > MaxFrames {
		return nil, fmt.Errorf("%w: frame count %d outside [1,%d]", ErrInvalidPacket, h.Frames, MaxFrames)
	}
	if h.Payload > MaxPayload {
		return nil, fmt.Errorf("%w: payload length %d exceeds %d", ErrInvalidPacket, h.Payload, MaxPayload)
	}
	if len(buf) != HeaderLen+h.Payload {
		return nil, fmt.Errorf("%w: length %d does not match header payload %d", ErrInvalidPacket, len(buf), h.Payload)
	}
	want := binary.BigEndian.Uint16(buf[14:16])
	got := Checksum(buf[:14], buf[HeaderLen:])
	if want != got {
		return nil, fmt.Errorf("%w: checksum %04x != %04x", ErrInvalidPacket, got, want)
	}
	return &Packet{Header: h, Payload: append([]byte(nil), buf[HeaderLen:]...)}, nil
}

// Checksum computes CRC-16/CCITT-FALSE over header and payload.
func Checksum(header, payload []byte) uint16 {
	var crc uint16 = 0xffff
	for _, b := range header {
		crc = crc16Byte(crc, b)
	}
	for _, b := range payload {
		crc = crc16Byte(crc, b)
	}
	return crc
}

func crc16Byte(crc uint16, b byte) uint16 {
	crc ^= uint16(b) << 8
	for i := 0; i < 8; i++ {
		if crc&0x8000 != 0 {
			crc = crc<<1 ^ 0x1021
		} else {
			crc <<= 1
		}
	}
	return crc
}
