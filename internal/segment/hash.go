// Package segment computes deterministic per-segment content hashes.
//
// A segment hash covers exactly the data delivered for that segment — the
// aligned sample timestamps, keyframe flags, and payload bytes — in a fixed
// byte order with explicit length prefixes. Re-running the same input yields
// the same hashes, and a repaired loss changes only the hash of the segment
// that contained the damage.
package segment

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"

	"example.com/mediaforge/internal/merrors"
)

// MaxSamplesPerSegment bounds a segment's sample count.
const MaxSamplesPerSegment = 100_000

// Sample is one aligned media unit contributing to a segment hash.
type Sample struct {
	TrackSSRC  uint32
	CommonTick uint64
	Duration   uint64
	Keyframe   bool
	// Recovered marks a sample reconstructed from parity. The flag is hashed
	// so a repaired delivery is distinguishable, yet deterministic.
	Recovered bool
	Payload   []byte
}

// Segment is one deliverable piece.
type Segment struct {
	Index     int
	StartTick uint64
	EndTick   uint64
	Samples   []Sample
}

// Hash is the result for one segment.
type Hash struct {
	Index     int
	StartTick uint64
	EndTick   uint64
	Samples   int
	Bytes     int
	SHA256    [32]byte
}

// Hex renders the digest.
func (h Hash) Hex() string { return fmt.Sprintf("%x", h.SHA256[:]) }

// MaxPayloadBytes bounds total bytes hashed per segment.
const MaxPayloadBytes = 32 << 20

// Compute hashes a segment. The byte stream is:
//
//	"mfs1" | segment index (u32) | start tick (u64) | end tick (u64) |
//	for each sample: ssrc (u32) | tick (u64) | duration (u64) |
//	                 flags (u8) | payload length (u32) | payload
//
// All integers are big-endian. The version tag means a future change to the
// layout cannot silently collide with existing hashes.
func Compute(seg Segment) (Hash, error) {
	if seg.Index < 0 {
		return Hash{}, merrors.New(merrors.StageSegment, merrors.Invalid, "negative segment index")
	}
	if seg.EndTick < seg.StartTick {
		return Hash{}, merrors.New(merrors.StageSegment, merrors.Invalid,
			"segment %d ends before it starts", seg.Index)
	}
	if len(seg.Samples) > MaxSamplesPerSegment {
		return Hash{}, merrors.New(merrors.StageSegment, merrors.Bounds,
			"segment %d has %d samples, limit %d", seg.Index, len(seg.Samples), MaxSamplesPerSegment)
	}

	h := sha256.New()
	writeTag(h)
	var scratch [8]byte
	putU32(h, scratch[:], uint32(seg.Index))
	putU64(h, scratch[:], seg.StartTick)
	putU64(h, scratch[:], seg.EndTick)

	total := 0
	for i, s := range seg.Samples {
		if len(s.Payload) > MaxPayloadBytes-total {
			return Hash{}, merrors.New(merrors.StageSegment, merrors.Bounds,
				"segment %d payload bytes exceed %d", seg.Index, MaxPayloadBytes)
		}
		total += len(s.Payload)
		putU32(h, scratch[:], s.TrackSSRC)
		putU64(h, scratch[:], s.CommonTick)
		putU64(h, scratch[:], s.Duration)
		var flags byte
		if s.Keyframe {
			flags |= 1
		}
		if s.Recovered {
			flags |= 2
		}
		h.Write([]byte{flags})
		putU32(h, scratch[:], uint32(len(s.Payload)))
		h.Write(s.Payload)
		_ = i
	}

	out := Hash{
		Index:     seg.Index,
		StartTick: seg.StartTick,
		EndTick:   seg.EndTick,
		Samples:   len(seg.Samples),
		Bytes:     total,
	}
	copy(out.SHA256[:], h.Sum(nil))
	return out, nil
}

// ComputeAll hashes every segment in order.
func ComputeAll(segs []Segment) ([]Hash, error) {
	out := make([]Hash, 0, len(segs))
	seen := make(map[int]bool, len(segs))
	for _, s := range segs {
		if seen[s.Index] {
			return nil, merrors.New(merrors.StageSegment, merrors.Invalid,
				"duplicate segment index %d", s.Index)
		}
		seen[s.Index] = true
		h, err := Compute(s)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// Equal compares two hash lists, requiring the same length and digests in
// order. It returns the first differing index, or -1.
func Equal(a, b []Hash) (int, bool) {
	if len(a) != len(b) {
		return min(len(a), len(b)), false
	}
	for i := range a {
		if a[i].SHA256 != b[i].SHA256 {
			return i, false
		}
	}
	return -1, true
}

// Verify checks a hash list against a rule that a segment's hash covers
// exactly its samples and bounds, catching internal inconsistency.
func Verify(segs []Segment, hashes []Hash) error {
	if len(segs) != len(hashes) {
		return merrors.New(merrors.StageSegment, merrors.Invalid,
			"%d segments but %d hashes", len(segs), len(hashes))
	}
	for i := range segs {
		want, err := Compute(segs[i])
		if err != nil {
			return err
		}
		if want.SHA256 != hashes[i].SHA256 {
			return merrors.At(merrors.StageSegment, merrors.Checksum, int64(i),
				"segment %d hash mismatch", segs[i].Index)
		}
	}
	return nil
}

func writeTag(h hash.Hash) { h.Write([]byte("mfs1")) }

func putU32(h hash.Hash, scratch []byte, v uint32) {
	binary.BigEndian.PutUint32(scratch[:4], v)
	h.Write(scratch[:4])
}

func putU64(h hash.Hash, scratch []byte, v uint64) {
	binary.BigEndian.PutUint64(scratch[:8], v)
	h.Write(scratch[:8])
}
