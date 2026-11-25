// Package sample deterministically generates all in-memory Matroska/WebM and
// RTP fixtures used by demos and tests. It never reads external files.
package sample

import (
	"encoding/binary"
	"math"
)

type ebbuilder struct {
	buf []byte
}

func (b *ebbuilder) write(p []byte) { b.buf = append(b.buf, p...) }

func idBytes(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// sizeVINT encodes a known EBML payload length using the minimal width.
func sizeVINT(size int) []byte {
	for width := 1; width <= 8; width++ {
		maxData := int((uint64(1) << (7 * width)) - 2) // all-one data is "unknown"
		if size <= maxData {
			raw := uint64(1)<<(7*width) | uint64(size)
			out := make([]byte, width)
			for i := width - 1; i >= 0; i-- {
				out[i] = byte(raw & 0xFF)
				raw >>= 8
			}
			return out
		}
	}
	panic("ebml size exceeds 8-byte VINT")
}

// unknownSizeVINT returns the reserved all-data-ones unknown-length encoding.
func unknownSizeVINT(width int) []byte {
	raw := (uint64(1) << (7*width + 1)) - 1
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(raw & 0xFF)
		raw >>= 8
	}
	return out
}

func element(id uint32, payload []byte) []byte {
	out := append([]byte{}, idBytes(id)...)
	out = append(out, sizeVINT(len(payload))...)
	return append(out, payload...)
}

func unknownElement(id uint32, width int) []byte {
	out := idBytes(id)
	return append(out, unknownSizeVINT(width)...)
}

func uintPayload(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	n := 8
	for n > 1 && tmp[8-n] == 0 {
		n--
	}
	start := 8 - n
	return append([]byte{}, tmp[start:8]...)
}

func uintElement(id uint32, v uint64) []byte {
	return element(id, uintPayload(v))
}

func stringElement(id uint32, s string) []byte {
	return element(id, []byte(s))
}

func floatElement(id uint32, v float64) []byte {
	var p [8]byte
	binary.BigEndian.PutUint64(p[:], math.Float64bits(v))
	return element(id, p[:])
}

func appendElement(dst []byte, id uint32, payload []byte) []byte {
	return append(dst, element(id, payload)...)
}

func appendUint(dst []byte, id uint32, v uint64) []byte {
	return append(dst, uintElement(id, v)...)
}

func appendString(dst []byte, id uint32, v string) []byte {
	return append(dst, stringElement(id, v)...)
}

func appendFloat(dst []byte, id uint32, v float64) []byte {
	return append(dst, floatElement(id, v)...)
}

// simpleBlock encodes a SimpleBlock payload. Track numbers in fixtures are
// small enough for the one-byte VINT form 0x80|number.
func simpleBlock(track uint64, relativeMS int16, flags byte, frame []byte) []byte {
	payload := []byte{byte(0x80 | track), byte(relativeMS >> 8), byte(relativeMS), flags}
	payload = append(payload, frame...)
	return element(0xA3, payload)
}
