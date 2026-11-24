// Package essence defines the self-describing elementary frame carried inside
// the generated MXF and MP4 fixtures. Every frame starts with a fixed header
// that records its owner track and original time range, which lets the
// recovery scanner rebuild a boundary-exact timeline after container damage.
//
// Layout (big endian):
//
//	 0..3   magic "ARCH"
//	 4      version (1)
//	 5      kind (0 video, 1 audio, 2 data)
//	 6..7   flags (bit 0: sync sample)
//	 8..11  track id
//	12..15  frame sequence within the track
//	16..23  start tick on the track clock
//	24..31  duration in ticks
//	32..35  payload length
//	36..39  CRC32 (IEEE) over bytes 0..35
//	40..    payload
//	last 4  CRC32 (IEEE) over the payload
package essence

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
)

// Magic is the 4-byte frame marker.
var Magic = [4]byte{'A', 'R', 'C', 'H'}

const (
	Version      = 1
	HeaderSize   = 40
	TrailerSize  = 4
	MinFrameSize = HeaderSize + TrailerSize
)

// Kind mirrors model.TrackKind numerically without importing it.
type Kind uint8

const (
	Video Kind = 0
	Audio Kind = 1
	Data  Kind = 2
)

// Header is the parsed frame prefix.
type Header struct {
	Version    uint8
	Kind       Kind
	Flags      uint16
	TrackID    uint32
	Seq        uint32
	StartTick  uint64
	Duration   uint64
	PayloadLen uint32
	Sync       bool
}

// TotalSize is the number of bytes the frame occupies on disk.
func (h Header) TotalSize() uint64 {
	return HeaderSize + uint64(h.PayloadLen) + TrailerSize
}

// ErrShortFrame means the buffer is too small to contain a frame header.
var ErrShortFrame = errors.New("essence: short frame")

// ErrBadMagic means the buffer does not start with a frame marker.
var ErrBadMagic = errors.New("essence: bad magic")

// ErrBadHeaderCRC means the frame header was damaged.
var ErrBadHeaderCRC = errors.New("essence: header crc mismatch")

// Build serialises a frame from its parts.
func Build(h Header, payload []byte) []byte {
	h.Version = Version
	h.PayloadLen = uint32(len(payload))
	b := make([]byte, HeaderSize+len(payload)+TrailerSize)
	copy(b[0:4], Magic[:])
	b[4] = h.Version
	b[5] = byte(h.Kind)
	binary.BigEndian.PutUint16(b[6:8], h.Flags)
	binary.BigEndian.PutUint32(b[8:12], h.TrackID)
	binary.BigEndian.PutUint32(b[12:16], h.Seq)
	binary.BigEndian.PutUint64(b[16:24], h.StartTick)
	binary.BigEndian.PutUint64(b[24:32], h.Duration)
	binary.BigEndian.PutUint32(b[32:36], h.PayloadLen)
	binary.BigEndian.PutUint32(b[36:40], CRC(b[:36]))
	copy(b[HeaderSize:], payload)
	binary.BigEndian.PutUint32(b[HeaderSize+len(payload):], CRC(payload))
	return b
}

// ParseHeader validates and decodes the fixed prefix.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, ErrShortFrame
	}
	if !bytes.Equal(b[0:4], Magic[:]) {
		return Header{}, ErrBadMagic
	}
	if got, want := binary.BigEndian.Uint32(b[36:40]), CRC(b[:36]); got != want {
		return Header{}, ErrBadHeaderCRC
	}
	h := Header{
		Version:    b[4],
		Kind:       Kind(b[5]),
		Flags:      binary.BigEndian.Uint16(b[6:8]),
		TrackID:    binary.BigEndian.Uint32(b[8:12]),
		Seq:        binary.BigEndian.Uint32(b[12:16]),
		StartTick:  binary.BigEndian.Uint64(b[16:24]),
		Duration:   binary.BigEndian.Uint64(b[24:32]),
		PayloadLen: binary.BigEndian.Uint32(b[32:36]),
	}
	h.Sync = h.Flags&1 != 0
	if h.Version != Version {
		return Header{}, fmt.Errorf("essence: unsupported version %d", h.Version)
	}
	return h, nil
}

// VerifyPayload checks the trailing payload checksum. It returns false when
// the frame is cut short, making the payload indeterminable.
func VerifyPayload(b []byte, h Header) (bool, bool) {
	// complete reports whether the whole frame is present.
	if uint64(len(b)) < h.TotalSize() {
		return false, false
	}
	start := HeaderSize + int(h.PayloadLen)
	got := binary.BigEndian.Uint32(b[start : start+TrailerSize])
	want := CRC(b[HeaderSize:start])
	return got == want, true
}

// CRC is CRC32-IEEE.
func CRC(b []byte) uint32 { return crc32.ChecksumIEEE(b) }

// Payload builds deterministic pseudo-random payload bytes. Fixtures are then
// reproducible without shipping binary data files.
func Payload(seed uint64, n int) []byte {
	out := make([]byte, n)
	x := seed ^ 0x9e3779b97f4a7c15
	for i := range out {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		out[i] = byte(z)
	}
	return out
}

// ClampInt32 guards conversions when fixtures declare unreasonable sizes.
func ClampInt32(v int64) uint32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}
