// Package wire defines the 48 kHz / 20 ms mono PCM16 audio packet format
// and validates packets on decode. Malformed packets are rejected with
// typed errors so callers can count them and keep going.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	SampleRate      = 48000
	FrameMs         = 20
	SamplesPerFrame = SampleRate * FrameMs / 1000 // 960
	PayloadBytes    = SamplesPerFrame * 2

	magic      = 0x4A545231 // "JTR1"
	headerSize = 16         // magic(4) seq(2) timestamp(4) samples(2) checksum(4)

	// PacketSize is the exact wire size of one audio packet.
	PacketSize = headerSize + PayloadBytes
)

var (
	ErrTooShort    = errors.New("wire: packet too short")
	ErrBadLength   = errors.New("wire: bad packet length")
	ErrBadMagic    = errors.New("wire: bad magic")
	ErrBadChecksum = errors.New("wire: checksum mismatch")
	ErrBadSamples  = errors.New("wire: unexpected sample count")
)

// Packet is one 20 ms mono PCM16 audio frame.
type Packet struct {
	Seq       uint16 // wraps every 65536 frames
	Timestamp uint32 // sample units, SamplesPerFrame per frame
	Samples   uint16
	Payload   []byte // little-endian PCM16, exactly PayloadBytes
}

// Encode serializes p and appends a CRC32 checksum over header+payload.
func Encode(p Packet) ([]byte, error) {
	if len(p.Payload) != PayloadBytes {
		return nil, fmt.Errorf("wire: payload is %d bytes, want %d", len(p.Payload), PayloadBytes)
	}
	buf := make([]byte, PacketSize)
	binary.BigEndian.PutUint32(buf[0:4], magic)
	binary.BigEndian.PutUint16(buf[4:6], p.Seq)
	binary.BigEndian.PutUint32(buf[6:10], p.Timestamp)
	binary.BigEndian.PutUint16(buf[10:12], p.Samples)
	copy(buf[headerSize:], p.Payload)
	binary.BigEndian.PutUint32(buf[12:16], checksum(buf))
	return buf, nil
}

// Decode parses and validates one packet: exact length, magic, checksum
// and sample count are all verified.
func Decode(b []byte) (Packet, error) {
	var p Packet
	if len(b) < headerSize {
		return p, ErrTooShort
	}
	if len(b) != PacketSize {
		return p, ErrBadLength
	}
	if binary.BigEndian.Uint32(b[0:4]) != magic {
		return p, ErrBadMagic
	}
	if binary.BigEndian.Uint32(b[12:16]) != checksum(b) {
		return p, ErrBadChecksum
	}
	p.Seq = binary.BigEndian.Uint16(b[4:6])
	p.Timestamp = binary.BigEndian.Uint32(b[6:10])
	p.Samples = binary.BigEndian.Uint16(b[10:12])
	if p.Samples != SamplesPerFrame {
		return p, ErrBadSamples
	}
	p.Payload = make([]byte, PayloadBytes)
	copy(p.Payload, b[headerSize:])
	return p, nil
}

// checksum covers the header without the checksum field plus the payload.
func checksum(b []byte) uint32 {
	h := crc32.NewIEEE()
	h.Write(b[0:12])
	h.Write(b[headerSize:])
	return h.Sum32()
}

// PCMToBytes converts PCM16 samples to little-endian bytes.
func PCMToBytes(pcm []int16) []byte {
	b := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

// BytesToPCM converts little-endian bytes to PCM16 samples.
func BytesToPCM(b []byte) []int16 {
	pcm := make([]int16, len(b)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return pcm
}
