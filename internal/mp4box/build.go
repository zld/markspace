// Package mp4box builds ISO BMFF boxes byte-for-byte. It is shared by the
// clip writer and the fixture generator so both emit the same layout and the
// parser can be round-tripped deterministically.
package mp4box

import "encoding/binary"

// Box prepends a 32-bit size and fourcc to payload.
func Box(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:8], typ)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

// FullBox prepends size, type, version and 24-bit flags.
func FullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	n := 12
	for _, p := range payload {
		n += len(p)
	}
	out := make([]byte, 12, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:8], typ)
	out[8] = version
	out[9] = byte(flags >> 16)
	out[10] = byte(flags >> 8)
	out[11] = byte(flags)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

// Container concatenates child boxes into one box of the given type.
func Container(typ string, children ...[]byte) []byte {
	return Box(typ, children...)
}

// U8 encodes one byte.
func U8(v byte) []byte { return []byte{v} }

// U16 encodes a big-endian uint16.
func U16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// I16 encodes a big-endian int16.
func I16(v int16) []byte { return U16(uint16(v)) }

// U24 encodes a big-endian uint24.
func U24(v uint32) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }

// U32 encodes a big-endian uint32.
func U32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// I32 encodes a big-endian int32.
func I32(v int32) []byte { return U32(uint32(v)) }

// U64 encodes a big-endian uint64.
func U64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// Zeros returns n zero bytes.
func Zeros(n int) []byte { return make([]byte, n) }

// Fixed1616 encodes an unsigned 16.16 fixed-point value.
func Fixed1616(v uint32) []byte { return U32(v << 16) }

// Fixed88 encodes a signed 8.8 fixed-point value in 16 bits.
func Fixed88(v int16) []byte { return I16(v << 8) }

// Matrix is the identity transformation matrix.
func Matrix() []byte {
	m := [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	out := make([]byte, 0, 36)
	for _, v := range m {
		out = append(out, I32(v)...)
	}
	return out
}
