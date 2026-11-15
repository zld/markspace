// Package protocol defines the fixed-format audio datagram used by jbbench:
// a fixed binary header followed by 16-bit little-endian PCM samples.
package protocol

import (
	"encoding/binary"
	"errors"
	"time"
)

// Fixed stream parameters.
const (
	Version        = 1
	SampleRate     = 16000 // samples per second
	FrameDuration  = 20    // milliseconds per frame
	SamplesPerFrame = SampleRate * FrameDuration / 1000 // 320
	PayloadBytes   = SamplesPerFrame * 2                // 640
	HeaderSize     = 24
	MaxPacketSize  = HeaderSize + PayloadBytes
	MinPacketSize  = HeaderSize
)

// Sentinel decode errors. Each maps to an independent drop counter.
var (
	ErrShort         = errors.New("protocol: datagram shorter than header")
	ErrUnknownVersion = errors.New("protocol: unknown protocol version")
	ErrIllegalLength = errors.New("protocol: illegal frame payload length")
	ErrChecksum      = errors.New("protocol: checksum mismatch")
	ErrAmbiguousSeq  = errors.New("protocol: sequence number half-interval ambiguity")
)

// Packet is one decoded audio datagram.
type Packet struct {
	Version uint8
	Flags   uint8
	Seq     uint16 // wraps modulo 65536
	FrameNo uint32 // monotonic frame index, never wraps during a run
	SendNS  uint64 // sender timestamp in nanoseconds
	Length  uint16 // payload length in bytes
	Samples []int16
}

// FlagSilence marks a sender-generated silence frame.
const FlagSilence uint8 = 1 << 0

// Encode serializes the packet into dst (must be at least HeaderSize+len).
func Encode(dst []byte, p Packet) int {
	total := HeaderSize + len(p.Samples)*2
	if len(dst) < total {
		panic("protocol: encode buffer too small")
	}
	dst[0] = p.Version
	dst[1] = p.Flags
	binary.BigEndian.PutUint16(dst[2:4], p.Seq)
	binary.BigEndian.PutUint32(dst[4:8], p.FrameNo)
	binary.BigEndian.PutUint64(dst[8:16], p.SendNS)
	binary.BigEndian.PutUint16(dst[16:18], uint16(len(p.Samples)*2))
	binary.BigEndian.PutUint16(dst[18:20], 0) // reserved
	binary.BigEndian.PutUint32(dst[20:24], 0) // checksum placeholder
	for i, s := range p.Samples {
		binary.LittleEndian.PutUint16(dst[HeaderSize+2*i:], uint16(s))
	}
	binary.BigEndian.PutUint32(dst[20:24], Checksum(dst[:total]))
	return total
}

// Checksum is a 32-bit FNV-1a hash over the whole datagram with the checksum
// field zeroed.
func Checksum(b []byte) uint32 {
	const (
		offset = uint32(2166136261)
		prime  = uint32(16777619)
	)
	h := offset
	for i := range b {
		if i >= 20 && i < 24 {
			continue
		}
		h ^= uint32(b[i])
		h *= prime
	}
	return h
}

// Decode parses a datagram, validating version, length and checksum.
// On success the returned packet aliases b for its sample payload.
func Decode(b []byte) (Packet, error) {
	if len(b) < MinPacketSize {
		return Packet{}, ErrShort
	}
	var p Packet
	p.Version = b[0]
	if p.Version != Version {
		return Packet{}, ErrUnknownVersion
	}
	p.Flags = b[1]
	p.Seq = binary.BigEndian.Uint16(b[2:4])
	p.FrameNo = binary.BigEndian.Uint32(b[4:8])
	p.SendNS = binary.BigEndian.Uint64(b[8:16])
	p.Length = binary.BigEndian.Uint16(b[16:18])
	if p.Length != PayloadBytes || len(b) != HeaderSize+PayloadBytes {
		return Packet{}, ErrIllegalLength
	}
	if binary.BigEndian.Uint32(b[20:24]) != Checksum(b) {
		return Packet{}, ErrChecksum
	}
	p.Samples = make([]int16, SamplesPerFrame)
	for i := range p.Samples {
		p.Samples[i] = int16(binary.LittleEndian.Uint16(b[HeaderSize+2*i:]))
	}
	return p, nil
}

// SeqCompare performs modular sequence comparison and reports half-interval
// ambiguity (exact opposite distance). d>0 means a is newer than b.
func SeqCompare(a, b uint16) (d int, ambiguous bool) {
	diff := int16(a - b)
	if diff == 0 {
		return 0, false
	}
	if diff == -32768 {
		return 0, true
	}
	if diff < 0 {
		return int(diff), false
	}
	return int(diff), false
}

// FrameDurationNS is the fixed frame duration as a nanosecond duration.
func FrameDurationNS() time.Duration {
	return time.Duration(FrameDuration) * time.Millisecond
}

// SamplesBytes copies int16 samples into little-endian bytes.
func SamplesBytes(s []int16) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out
}
