// Package repair turns raw demuxed blocks into a monotonic, track-aligned
// stream. This file validates a rebuilt index and hashes it canonically.
package repair

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// DigestIndex returns the canonical SHA-256 of an index. Entries are hashed
// in stream order as (track, time, offset) triples in big-endian form.
func DigestIndex(entries []media.IndexEntry) string {
	h := sha256.New()
	var buf [8]byte
	for _, e := range entries {
		binary.BigEndian.PutUint64(buf[:], uint64(e.Track))
		h.Write(buf[:])
		binary.BigEndian.PutUint64(buf[:], uint64(e.TimeNs))
		h.Write(buf[:])
		binary.BigEndian.PutUint64(buf[:], uint64(e.ByteOffset))
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateIndex checks internal invariants: ordered, unique, and every entry
// carries a positive offset and a non-negative time.
func ValidateIndex(idx media.Index) error {
	var prev media.IndexEntry
	for i, e := range idx.Entries {
		if e.ByteOffset <= 0 {
			return merr.New("index.validate", merr.CodeInvalidIndex, e.ByteOffset,
				"entry %d has non-positive byte offset", i)
		}
		if e.TimeNs < 0 {
			return merr.New("index.validate", merr.CodeInvalidIndex, e.ByteOffset,
				"entry %d has negative time", i)
		}
		if i > 0 {
			if e.Track < prev.Track || (e.Track == prev.Track && e.TimeNs <= prev.TimeNs) {
				return merr.New("index.validate", merr.CodeInvalidIndex, e.ByteOffset,
					"entry %d not ordered after previous", i)
			}
			if e.Track == prev.Track && e.ByteOffset <= prev.ByteOffset {
				return merr.New("index.validate", merr.CodeInvalidIndex, e.ByteOffset,
					"entry %d offset does not advance", i)
			}
		}
		prev = e
	}
	if idx.Digest != "" && idx.Digest != DigestIndex(idx.Entries) {
		return merr.New("index.validate", merr.CodeInvalidIndex, -1,
			"digest does not match entries")
	}
	return nil
}

// EnsureKeyframesOnly verifies that every index entry points at a keyframe
// block in the given stream. It is the cross-stage invariant: the index must
// never reference a delta frame.
func EnsureKeyframesOnly(idx media.Index, blocks []media.Block) error {
	byOffset := make(map[int64]media.Block, len(blocks))
	for _, b := range blocks {
		byOffset[b.ElementOffset] = b
	}
	for i, e := range idx.Entries {
		b, ok := byOffset[e.ByteOffset]
		if !ok {
			return merr.New("index.keyframes", merr.CodeInvalidIndex, e.ByteOffset,
				"entry %d points at no block", i)
		}
		if b.TrackType != media.TrackVideo {
			return merr.New("index.keyframes", merr.CodeInvalidIndex, e.ByteOffset,
				"entry %d points at non-video track %d", i, b.Track)
		}
		if !b.Keyframe {
			return merr.New("index.keyframes", merr.CodeInvalidIndex, e.ByteOffset,
				"entry %d points at a delta frame", i)
		}
		if b.TimeNs != e.TimeNs {
			return merr.New("index.keyframes", merr.CodeInvalidIndex, e.ByteOffset,
				"entry %d time %d disagrees with block time %d", i, e.TimeNs, b.TimeNs)
		}
	}
	return nil
}
