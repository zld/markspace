// Package segment defines the repaired segment record, its deterministic
// canonical encoding and per-segment SHA-256 hashing.
package segment

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// IndexEntry is one random-access point inside a segment.
type IndexEntry struct {
	SampleIndex int   `json:"sample_index"`
	KeyframePID uint16 `json:"keyframe_pid"`
	// Time range covered by the sample, in nanoseconds on the repaired
	// shared clock. A query at target t hits when StartNanos <= t < EndNanos.
	StartNanos int64 `json:"start_nanos"`
	EndNanos   int64 `json:"end_nanos"`
	ByteOffset int64 `json:"byte_offset"`
}

// Segment is the fully repaired, boundary-checked record for one fragment of
// one representation.
type Segment struct {
	ID             int    `json:"id"`
	Representation int    `json:"representation"`
	SourceStart    int64  `json:"source_start"` // input byte range, inclusive
	SourceEnd      int64  `json:"source_end"`   // inclusive
	StartNanos     int64  `json:"start_nanos"`  // repaired shared-clock bounds
	EndNanos       int64  `json:"end_nanos"`
	SampleCount    int    `json:"sample_count"`
	KeyframeIndex  []IndexEntry `json:"keyframe_index"`
}

// Canonical encodes a segment into a deterministic, architecture-independent
// byte form. The encoding is length-prefixed and uses big-endian fixed-width
// integers, so identical segments always hash identically regardless of map
// iteration order or struct padding.
func Canonical(s *Segment) []byte {
	// 8 header int64s + 2 int32 counts; grown for index entries below.
	buf := make([]byte, 0, 64+len(s.KeyframeIndex)*34)
	put := func(v int64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(v))
		buf = append(buf, b[:]...)
	}
	put(int64(s.ID))
	put(int64(s.Representation))
	put(s.SourceStart)
	put(s.SourceEnd)
	put(s.StartNanos)
	put(s.EndNanos)
	put(int64(s.SampleCount))
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s.KeyframeIndex)))
	buf = append(buf, n[:]...)
	for _, e := range s.KeyframeIndex {
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], e.KeyframePID)
		buf = append(buf, p[:]...)
		var i [4]byte
		binary.BigEndian.PutUint32(i[:], uint32(e.SampleIndex))
		buf = append(buf, i[:]...)
		put(e.StartNanos)
		put(e.EndNanos)
		put(e.ByteOffset)
	}
	return buf
}

// Hash returns the hex-encoded SHA-256 of the canonical segment encoding.
func Hash(s *Segment) string {
	sum := sha256.Sum256(Canonical(s))
	return hex.EncodeToString(sum[:])
}

// HashBytes returns the raw 32-byte SHA-256 digest of the canonical encoding.
func HashBytes(s *Segment) [32]byte {
	return sha256.Sum256(Canonical(s))
}
