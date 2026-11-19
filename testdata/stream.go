package generator

import (
	"crypto/sha256"
	"encoding/binary"
)

// DeterministicStream returns an io.Reader that yields a reproducible,
// non-repeating byte stream derived from the seed and a domain-separation
// label. It lets seeded demo runs produce the same keys and nonces on every
// machine without weakening production mode, which uses crypto/rand.
//
// The stream is a counter-mode SHA-256 construction addressed by absolute
// block offset with an internal remainder, so output is identical no matter
// how the reader is chunked.
type DeterministicStream struct {
	key    [32]byte
	block  uint64
	buf    [32]byte
	filled int
}

// NewDeterministicStream builds a stream for the given seed and label.
func NewDeterministicStream(seed int64, label string) *DeterministicStream {
	h := sha256.New()
	h.Write([]byte("mediakey/stream/v1/"))
	h.Write([]byte(label))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(seed))
	h.Write(b[:])
	s := &DeterministicStream{}
	copy(s.key[:], h.Sum(nil))
	s.refill()
	return s
}

// refill advances to the next counter block.
func (s *DeterministicStream) refill() {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], s.block)
	h := sha256.New()
	h.Write(s.key[:])
	h.Write(counter[:])
	h.Sum(s.buf[:0])
	s.block++
	s.filled = 0
}

// Read fills p with stream bytes.
func (s *DeterministicStream) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if s.filled == len(s.buf) {
			s.refill()
		}
		copied := copy(p[n:], s.buf[s.filled:])
		s.filled += copied
		n += copied
	}
	return n, nil
}
