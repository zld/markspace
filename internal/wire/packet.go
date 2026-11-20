// Package wire defines the bounded, checksummed packet framing used on
// both the UDP and TCP receive paths.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	// Magic identifies jitterlab voice packets on the wire.
	Magic = uint16(0x4A4C) // "JL"
	// Version is the wire format version.
	Version = uint8(1)
	// HeaderSize is the fixed packet header length in bytes:
	// magic(2) version(1) flags(1) seq(2) timestamp(4) payloadLen(2) crc32(4).
	HeaderSize = 16
	// MaxPayload bounds a single packet payload (PCM frame is 1920 B).
	MaxPayload = 4096
	// MaxPacket is the largest accepted datagram/frame on the wire.
	MaxPacket = HeaderSize + MaxPayload
)

var (
	ErrShortBuffer  = errors.New("wire: buffer shorter than header")
	ErrBadMagic     = errors.New("wire: bad magic")
	ErrBadVersion   = errors.New("wire: unsupported version")
	ErrBadLength    = errors.New("wire: payload length out of range")
	ErrBadChecksum  = errors.New("wire: checksum mismatch")
	ErrPayloadLarge = errors.New("wire: payload exceeds maximum")
)

// Packet is one decoded voice packet.
type Packet struct {
	Seq       uint16 // 16-bit sequence number, wraps
	Timestamp uint32 // 32-bit sender timestamp in ms, wraps
	Payload   []byte // PCM payload (owned by caller; copied on Decode)
}

// Encode serializes p into a freshly allocated buffer.
func Encode(p Packet) ([]byte, error) {
	if len(p.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d > %d", ErrPayloadLarge, len(p.Payload), MaxPayload)
	}
	buf := make([]byte, HeaderSize+len(p.Payload))
	binary.BigEndian.PutUint16(buf[0:2], Magic)
	buf[2] = Version
	buf[3] = 0 // flags, reserved
	binary.BigEndian.PutUint16(buf[4:6], p.Seq)
	binary.BigEndian.PutUint32(buf[6:10], p.Timestamp)
	binary.BigEndian.PutUint16(buf[10:12], uint16(len(p.Payload)))
	copy(buf[HeaderSize:], p.Payload)
	crc := crc32.ChecksumIEEE(buf[:HeaderSize-4])
	crc = crc32.Update(crc, crc32.IEEETable, buf[HeaderSize:])
	binary.BigEndian.PutUint32(buf[12:16], crc)
	return buf, nil
}

// Decode parses and validates one packet from buf. buf must contain
// exactly one packet (length-prefixing for TCP happens in ingest).
func Decode(buf []byte) (Packet, error) {
	var p Packet
	if len(buf) < HeaderSize {
		return p, ErrShortBuffer
	}
	if binary.BigEndian.Uint16(buf[0:2]) != Magic {
		return p, ErrBadMagic
	}
	if buf[2] != Version {
		return p, ErrBadVersion
	}
	plen := int(binary.BigEndian.Uint16(buf[10:12]))
	if plen > MaxPayload {
		return p, fmt.Errorf("%w: %d > %d", ErrBadLength, plen, MaxPayload)
	}
	if len(buf) != HeaderSize+plen {
		return p, fmt.Errorf("%w: buffer %d, want %d", ErrBadLength, len(buf), HeaderSize+plen)
	}
	want := binary.BigEndian.Uint32(buf[12:16])
	crc := crc32.ChecksumIEEE(buf[:HeaderSize-4])
	crc = crc32.Update(crc, crc32.IEEETable, buf[HeaderSize:])
	if crc != want {
		return p, ErrBadChecksum
	}
	p.Seq = binary.BigEndian.Uint16(buf[4:6])
	p.Timestamp = binary.BigEndian.Uint32(buf[6:10])
	p.Payload = make([]byte, plen)
	copy(p.Payload, buf[HeaderSize:])
	return p, nil
}
