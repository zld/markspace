package ebml

import "bytes"

// EncodeVINT writes value as an EBML VINT of the given width (1..8). When
// width <= 0 the smallest width that holds the value is chosen.
func EncodeVINT(value uint64, width int) []byte {
	if width <= 0 {
		width = vintWidth(value)
	}
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(value & 0xFF)
		value >>= 8
	}
	out[0] |= 1 << (8 - uint(width)) // marker bit
	return out
}

func vintWidth(value uint64) int {
	// Seven payload bits per byte; values need room below the marker.
	for w := 1; w <= 8; w++ {
		max := uint64(1)<<(7*w) - 2
		if value <= max {
			return w
		}
	}
	return 8
}

// EncodeID writes an element identifier as a VINT (all bits retained, so the
// width is derived from the highest set byte).
func EncodeID(id uint32) []byte {
	width := 1
	switch {
	case id > 0xFFFFFF:
		width = 4
	case id > 0xFFFF:
		width = 3
	case id > 0xFF:
		width = 2
	}
	out := make([]byte, width)
	v := id
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(v & 0xFF)
		v >>= 8
	}
	return out
}

// ElementBytes assembles a complete EBML element with an explicit size.
func ElementBytes(id uint32, payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write(EncodeID(id))
	buf.Write(EncodeVINT(uint64(len(payload)), 0))
	buf.Write(payload)
	return buf.Bytes()
}

// ElementBytesUnknown assembles an element whose size VINT is all ones
// (unknown length), as used by streaming Segments and Clusters. The size
// VINT uses its smallest width (one byte); the ID width is unrelated.
func ElementBytesUnknown(id uint32, payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write(EncodeID(id))
	buf.Write([]byte{0xFF})
	buf.Write(payload)
	return buf.Bytes()
}

// UintBytes encodes an unsigned-integer payload.
func UintBytes(v uint64) []byte {
	if v == 0 {
		return []byte{0}
	}
	var raw [8]byte
	n := 0
	for x := v; x > 0; x >>= 8 {
		raw[7-n] = byte(x & 0xFF)
		n++
	}
	return raw[8-n:]
}

// StringBytes returns the payload encoding for a string element.
func StringBytes(s string) []byte { return []byte(s) }
