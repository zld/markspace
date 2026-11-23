package ingest

import (
	"crypto/sha256"

	"mediacore/media"
)

// MaxIndexEntries bounds the keyframe index so memory stays constant
// regardless of claimed stream duration.
const MaxIndexEntries = 1 << 16

// KeyframeIndex is the capture side's claim about where keyframes live.
// Entries are (offset, size) pairs into the raw stream; Digest is the
// SHA-256 of the exact stream bytes the index was built against.
type KeyframeIndex struct {
	Offsets []int64
	Sizes   []int64
	Digest  [sha256.Size]byte
}

// DigestOf computes the stream digest an index must match.
func DigestOf(stream []byte) [sha256.Size]byte {
	return sha256.Sum256(stream)
}

// ValidateIndex rejects an index that is malformed or does not match the
// stream it claims to describe. Every rejection is CodeIndexInvalid with
// the offending entry offset. Checks, in order:
//  1. entry count within hard bound, Offsets/Sizes same length
//  2. digest matches the stream (an index for other bytes is invalid)
//  3. offsets strictly increasing, non-negative, inside the stream
//  4. entries do not overlap and end within the stream
//  5. each indexed unit is actually a keyframe per the codec syntax
func ValidateIndex(idx *KeyframeIndex, stream []byte, codec Codec) error {
	const op = "ingest.ValidateIndex"
	if len(idx.Offsets) != len(idx.Sizes) {
		return media.Fault(media.CodeIndexInvalid, op, -1,
			"offsets (%d) and sizes (%d) length mismatch", len(idx.Offsets), len(idx.Sizes))
	}
	if len(idx.Offsets) > MaxIndexEntries {
		return media.Fault(media.CodeIndexInvalid, op, -1,
			"entry count %d exceeds %d", len(idx.Offsets), MaxIndexEntries)
	}
	if DigestOf(stream) != idx.Digest {
		return media.Fault(media.CodeIndexInvalid, op, -1,
			"stream digest does not match index digest")
	}
	units, err := Scan(stream, codec)
	if err != nil {
		return err // already classified
	}
	keyAt := make(map[int64]bool, len(units))
	for _, u := range units {
		if u.Keyframe {
			keyAt[u.Offset] = true
		}
	}
	prevEnd := int64(-1)
	for i := range idx.Offsets {
		off, size := idx.Offsets[i], idx.Sizes[i]
		if off < 0 || size <= 0 {
			return media.Fault(media.CodeIndexInvalid, op, off,
				"entry %d: negative offset or non-positive size %d", i, size)
		}
		if off >= int64(len(stream)) || off+size > int64(len(stream)) {
			return media.Fault(media.CodeIndexInvalid, op, off,
				"entry %d: range [%d,%d) outside stream of %d bytes", i, off, off+size, len(stream))
		}
		if prevEnd >= 0 && off < prevEnd {
			return media.Fault(media.CodeIndexInvalid, op, off,
				"entry %d: overlaps previous entry ending at %d", i, prevEnd)
		}
		if !keyAt[off] {
			return media.Fault(media.CodeIndexInvalid, op, off,
				"entry %d: no keyframe unit at offset", i)
		}
		prevEnd = off + size
	}
	return nil
}
