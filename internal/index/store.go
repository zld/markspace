package index

import (
	"encoding/binary"
	"hash/fnv"
)

// hasher renders a bounded-size, order-sensitive FNV-1a digest. It is used for
// the index digest and for per-segment hashes so that identical inputs produce
// identical hex strings across runs and platforms.
type hasher struct {
	h interface {
		Write([]byte) (int, error)
		Sum64() uint64
	}
	buf [8]byte
}

func newHasher() *hasher { return &hasher{h: fnv.New64a()} }

func (h *hasher) addString(s string) {
	_, _ = h.h.Write([]byte(s))
	_, _ = h.h.Write([]byte{0})
}

func (h *hasher) addInt(v int64) {
	binary.BigEndian.PutUint64(h.buf[:], uint64(v))
	_, _ = h.h.Write(h.buf[:])
}

func (h *hasher) addBytes(b []byte) {
	_, _ = h.h.Write(b)
}

func (h *hasher) hex() string {
	return hexUint64(h.h.Sum64())
}

// HashBytes returns the FNV-1a 64-bit hex digest of b. Segment hashes use the
// same primitive so that an index digest and a segment hash are comparable.
func HashBytes(b []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return hexUint64(h.Sum64())
}

func hexUint64(v uint64) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		out[i] = digits[v&0xF]
		v >>= 4
	}
	return string(out)
}
