package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// hasher accumulates a SHA-256 digest over length-prefixed fields. A SHA-256
// digest is used (rather than a short hash) because the same primitive backs
// the per-segment sha256 values the demo prints.
type hasher struct {
	buf []byte
}

func newHasher() *hasher { return &hasher{buf: make([]byte, 0, 256)} }

func (h *hasher) addString(s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.buf = append(h.buf, n[:]...)
	h.buf = append(h.buf, s...)
}

func (h *hasher) addInt(v int64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	h.buf = append(h.buf, n[:]...)
}

func (h *hasher) hex() string {
	sum := sha256.Sum256(h.buf)
	return hex.EncodeToString(sum[:])
}

// HashBytes returns the SHA-256 hex digest of b. Segment hashes use this so an
// index digest and a segment hash are the same width and primitive.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
