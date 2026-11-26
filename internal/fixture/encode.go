package fixture

import (
	"encoding/binary"
	"math"
)

// VINTSize encodes size as the shortest known-length EBML size VINT.
func VINTSize(size uint64) []byte {
	for width := 1; width <= 8; width++ {
		valueBits := 7 * width
		maxKnown := uint64(1)<<uint(valueBits) - 2
		if size > maxKnown {
			continue
		}
		buf := make([]byte, width)
		v := size
		for i := width - 1; i >= 0; i-- {
			buf[i] = byte(v & 0xff)
			v >>= 8
		}
		buf[0] |= byte(1 << uint(8-width))
		return buf
	}
	panic("EBML size exceeds 56-bit range")
}

// UnknownVINT encodes the all-ones unknown-length VINT of the given width.
func UnknownVINT(width int) []byte {
	if width < 1 || width > 8 {
		panic("invalid VINT width")
	}
	buf := make([]byte, width)
	for i := range buf {
		buf[i] = 0xff
	}
	buf[0] = byte(1 << uint(8-width))
	if width > 1 {
		// Preserve only value bits as ones; marker stays one and leading zero
		// bits are zero.
		buf[0] &= 0xff >> uint(width-1)
		buf[0] |= byte(1 << uint(8-width))
	} else {
		buf[0] = 0xff
	}
	return buf
}

// IDBytes encodes a known EBML element ID constant in its canonical width.
func IDBytes(id uint32) []byte {
	switch {
	case id >= 1<<24:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id >= 1<<16:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id >= 1<<8:
		return []byte{byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id)}
	}
}

// Element builds a known-length EBML element.
func Element(id uint32, payload []byte) []byte {
	var out []byte
	out = append(out, IDBytes(id)...)
	out = append(out, VINTSize(uint64(len(payload)))...)
	return append(out, payload...)
}

// UnknownElement builds an unknown-length EBML element with an explicit width.
func UnknownElement(id uint32, payload []byte, width int) []byte {
	var out []byte
	out = append(out, IDBytes(id)...)
	out = append(out, UnknownVINT(width)...)
	return append(out, payload...)
}

// UintElement builds an unsigned integer element.
func UintElement(id uint32, value uint64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	n := 8
	for n > 1 && raw[8-n] == 0 {
		n--
	}
	return Element(id, raw[8-n:])
}

// StringElement builds a UTF-8 string element.
func StringElement(id uint32, value string) []byte {
	return Element(id, []byte(value))
}

// FloatElement builds an 8-byte float element.
func FloatElement(id uint32, value float64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], math.Float64bits(value))
	return Element(id, raw[:])
}

// BlockPayload encodes a Matroska block header and payload.
func BlockPayload(track uint32, relativeTC int16, flags byte, payload []byte) []byte {
	out := append([]byte(nil), VINTSize(uint64(track))...)
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(relativeTC))
	out = append(out, tc[0], tc[1], flags)
	return append(out, payload...)
}

// SimpleBlockElement builds one SimpleBlock element.
func SimpleBlockElement(track uint32, relativeTC int16, keyframe bool, payload []byte) []byte {
	flags := byte(0)
	if keyframe {
		flags |= 0x80
	}
	return Element(0xA3, BlockPayload(track, relativeTC, flags, payload))
}
