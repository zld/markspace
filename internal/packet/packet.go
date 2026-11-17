// Package packet defines the wire format for jbcheck demo audio frames
// and the validation applied to every received datagram.
//
// Layout (big endian):
//
//	0  magic      uint16 (0x4A42)
//	2  seq        uint16 (wraps at 65536)
//	4  timestamp  uint32 (samples at 48 kHz, 480 per 10 ms frame)
//	8  payloadLen uint16 (bytes)
//	10 crc32      uint32 (IEEE, over bytes 0..10 and payload)
//	14 payload    PCM16 little-endian samples
package packet

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

const (
	Magic      uint16 = 0x4A42
	HeaderSize        = 14
	MaxPayload        = 1024
	MaxPacket         = HeaderSize + MaxPayload
)

var (
	ErrTruncated = errors.New("packet: truncated datagram")
	ErrTooLong   = errors.New("packet: datagram exceeds maximum size")
	ErrBadMagic  = errors.New("packet: bad magic")
	ErrBadLength = errors.New("packet: payload length mismatch")
	ErrBadCRC    = errors.New("packet: checksum mismatch")
)

// Packet is a decoded audio frame datagram.
type Packet struct {
	Seq       uint16
	Timestamp uint32
	Payload   []byte
}

// Encode serializes one frame. Payloads larger than MaxPayload are rejected.
func Encode(seq uint16, ts uint32, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, ErrTooLong
	}
	buf := make([]byte, HeaderSize+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], Magic)
	binary.BigEndian.PutUint16(buf[2:4], seq)
	binary.BigEndian.PutUint32(buf[4:8], ts)
	binary.BigEndian.PutUint16(buf[8:10], uint16(len(payload)))
	copy(buf[HeaderSize:], payload)
	crc := crc32.Update(0, crc32.IEEETable, buf[0:10])
	crc = crc32.Update(crc, crc32.IEEETable, payload)
	binary.BigEndian.PutUint32(buf[10:14], crc)
	return buf, nil
}

// Decode parses and validates a datagram. Truncated, oversized, bad-magic
// and bad-checksum datagrams are rejected with distinct errors.
func Decode(buf []byte) (Packet, error) {
	if len(buf) < HeaderSize {
		return Packet{}, ErrTruncated
	}
	if len(buf) > MaxPacket {
		return Packet{}, ErrTooLong
	}
	if binary.BigEndian.Uint16(buf[0:2]) != Magic {
		return Packet{}, ErrBadMagic
	}
	n := int(binary.BigEndian.Uint16(buf[8:10]))
	if n != len(buf)-HeaderSize {
		return Packet{}, ErrBadLength
	}
	want := binary.BigEndian.Uint32(buf[10:14])
	crc := crc32.Update(0, crc32.IEEETable, buf[0:10])
	crc = crc32.Update(crc, crc32.IEEETable, buf[HeaderSize:])
	if crc != want {
		return Packet{}, ErrBadCRC
	}
	return Packet{
		Seq:       binary.BigEndian.Uint16(buf[2:4]),
		Timestamp: binary.BigEndian.Uint32(buf[4:8]),
		Payload:   buf[HeaderSize:],
	}, nil
}

// EncodePCM packs 48 kHz mono PCM16 samples into a payload.
func EncodePCM(samples []int16) []byte {
	buf := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(s))
	}
	return buf
}

// DecodePCM unpacks a payload into PCM16 samples. Odd lengths are corrupt.
func DecodePCM(payload []byte) ([]int16, error) {
	if len(payload)%2 != 0 {
		return nil, ErrBadLength
	}
	out := make([]int16, len(payload)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(payload[2*i:]))
	}
	return out, nil
}
