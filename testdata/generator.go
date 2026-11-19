// Package generator produces deterministic sample media segments, key seeds
// and corrupted copies for tests and the demo command. Nothing here reads
// external files: all sample content is synthesized from a seed.
package generator

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"
)

// Limits for a single demo or test run.
const (
	MaxSegments    = 10000
	DefaultSeed    = int64(20260420)
	DefaultSegSize = 4096
)

// Options controls sample generation. Every field is deterministic: the same
// options always produce identical bytes.
type Options struct {
	// Seeds drives both the segment content and the "random" streams used in
	// seeded demo mode for keys and nonces.
	Seed int64
	// SegmentCount is the number of segments to synthesize.
	SegmentCount int
	// SegmentSize is the nominal plaintext size of each segment in bytes.
	SegmentSize int
}

// Validate checks the options are inside the supported limits.
func (o Options) Validate() error {
	if o.SegmentCount <= 0 {
		return fmt.Errorf("generator: segment count must be positive, got %d", o.SegmentCount)
	}
	if o.SegmentCount > MaxSegments {
		return fmt.Errorf("generator: segment count %d exceeds limit %d", o.SegmentCount, MaxSegments)
	}
	if o.SegmentSize <= 0 {
		return fmt.Errorf("generator: segment size must be positive, got %d", o.SegmentSize)
	}
	return nil
}

// Segment is one synthesized media chunk.
type Segment struct {
	Index int
	Data  []byte
}

// Generate returns n deterministic segments. Segment i varies in length by a
// small amount so the manifest and ciphertext sizes are not all identical,
// while remaining fully reproducible from the seed.
func Generate(o Options) ([]Segment, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	out := make([]Segment, o.SegmentCount)
	for i := 0; i < o.SegmentCount; i++ {
		size := o.SegmentSize + int(hashInt(o.Seed, i)%257)
		out[i] = Segment{Index: i, Data: segmentBytes(o.Seed, i, size)}
	}
	return out, nil
}

func segmentBytes(seed int64, index, size int) []byte {
	buf := make([]byte, size)
	var header [16]byte
	binary.BigEndian.PutUint64(header[0:8], uint64(seed))
	binary.BigEndian.PutUint64(header[8:16], uint64(index))
	for off := 0; off < size; {
		h := sha256.New()
		h.Write(header[:])
		var counter [8]byte
		binary.BigEndian.PutUint64(counter[:], uint64(off))
		h.Write(counter[:])
		off += copy(buf[off:], h.Sum(nil))
	}
	return buf
}

func hashInt(seed int64, index int) int64 {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], uint64(seed))
	binary.BigEndian.PutUint64(b[8:16], uint64(index))
	sum := sha256.Sum256(b[:])
	return int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
}

// Corrupt flips one byte in the middle of data, simulating bit rot in an
// archived segment. The original slice is not modified.
func Corrupt(data []byte) []byte {
	if len(data) == 0 {
		return []byte{0x01}
	}
	out := make([]byte, len(data))
	copy(out, data)
	out[len(out)/2] ^= 0xff
	return out
}

// Drop returns segments with the segment at index removed. The remaining
// segments keep their original indices, which models a missing file rather
// than a renumbered set.
func Drop(segments []Segment, index int) []Segment {
	out := make([]Segment, 0, len(segments))
	for _, s := range segments {
		if s.Index == index {
			continue
		}
		out = append(out, s)
	}
	return out
}

// DeterministicSeedMaterial derives a fixed key/signing seed from an integer
// seed. It is only used in seeded demo mode; production mode uses crypto/rand.
func DeterministicSeedMaterial(seed int64) []byte {
	h := sha256.New()
	h.Write([]byte("mediakey/seed/v1"))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(seed))
	h.Write(b[:])
	return h.Sum(nil)
}

// BaseTime is the fixed wall-clock anchor used by seeded demo runs so
// boundary times are reproducible across machines.
var BaseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
