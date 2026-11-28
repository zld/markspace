package testmedia

import "encoding/binary"

// TruncateAt returns a copy of b shortened to n bytes. When n < len(b) the
// final box header still declares its original size, so parsers must report a
// stable truncation error with the offending offset.
func TruncateAt(b []byte, n int) []byte {
	if n < 0 {
		n = 0
	}
	if n > len(b) {
		n = len(b)
	}
	out := make([]byte, n)
	copy(out, b[:n])
	return out
}

// InflateBoxSize rewrites the 32-bit size field of the top-level box whose
// header starts at boxOffset so it extends beyond end of file. The returned
// slice keeps the original length, simulating a truncated container while
// leaving every earlier box intact.
func InflateBoxSize(b []byte, boxOffset int) []byte {
	out := append([]byte(nil), b...)
	if boxOffset < 0 || boxOffset+8 > len(out) {
		panic("testmedia: box offset out of range")
	}
	binary.BigEndian.PutUint32(out[boxOffset:], uint32(len(out))+4096)
	return out
}

// FindTopLevelBox returns the header offset of the first top-level box of the
// given fourCC, or -1 when absent.
func FindTopLevelBox(b []byte, typ string) int {
	return indexOfBox(b, typ)
}

// indexOfBox returns the offset of a top-level box header, or -1.
func indexOfBox(b []byte, typ string) int {
	for off := 0; off+8 <= len(b); {
		size := int(uint32(b[off])<<24 | uint32(b[off+1])<<16 |
			uint32(b[off+2])<<8 | uint32(b[off+3]))
		if string(b[off+4:off+8]) == typ {
			return off
		}
		if size <= 0 || off+size > len(b) {
			return -1
		}
		off += size
	}
	return -1
}
