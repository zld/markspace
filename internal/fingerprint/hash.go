// Package fingerprint computes content hashes for media segments and
// performs content-addressed deduplication. Two references are merged only
// when their SHA-256 content hashes are identical; every source offset is
// retained so a merged entry still traces back to every input byte.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"mediascan/internal/errs"
)

// RuleVersion is bumped whenever hashing or dedup semantics change, so a
// checkpoint written under an older rule is not silently reused.
const RuleVersion = "rules-v1"

// ByteRange is a half-open [Start, End) interval inside a source object.
type ByteRange struct {
	Start int64
	End   int64
}

// TimeRange is a half-open [Start, End) interval on the segment timescale.
type TimeRange struct {
	Start int64
	End   int64
	Scale uint32
}

// SourceRef is one place where a piece of content physically lives.
type SourceRef struct {
	Source    string
	ByteRange ByteRange
	Segment   int64
	TrackID   uint32
}

// Record is the per-segment fingerprint entry.
type Record struct {
	// Hash is the hex SHA-256 of the exact segment bytes.
	Hash string
	// ByteLength is the hashed byte count.
	ByteLength int64
	// TimeRange locates the segment on its track timeline.
	TimeRange TimeRange
	// KeyframeHit marks whether a confirmed keyframe lands in this segment.
	KeyframeHit bool
	// Refs are every source location carrying identical content. A single
	// content hash can back several references after dedup.
	Refs []SourceRef
}

// Hasher accumulates segment records and merges identical content.
type Hasher struct {
	records map[string]*Record
	order   []string // hash insertion order for deterministic output
}

// NewHasher creates an empty hasher.
func NewHasher() *Hasher {
	return &Hasher{records: map[string]*Record{}}
}

// Sum returns the hex SHA-256 of data, centralized so no other package
// picks a different digest.
func Sum(data []byte) string {
	d := sha256.Sum256(data)
	return hex.EncodeToString(d[:])
}

// Add hashes one segment's bytes and registers its source reference.
// Content-identical segments merge onto the same record; distinct content
// always gets a distinct record even if the time ranges coincide.
func (h *Hasher) Add(data []byte, tr TimeRange, keyframe bool, ref SourceRef) (*Record, error) {
	if len(data) == 0 {
		return nil, errs.New(errs.KindSegment, ref.Source, ref.ByteRange.Start,
			ref.Segment, ref.TrackID, "refusing to hash empty segment")
	}
	if ref.ByteRange.End < ref.ByteRange.Start {
		return nil, errs.New(errs.KindSegment, ref.Source, ref.ByteRange.Start,
			ref.Segment, ref.TrackID, "inverted byte range [%d,%d)",
			ref.ByteRange.Start, ref.ByteRange.End)
	}
	hash := Sum(data)
	rec, ok := h.records[hash]
	if !ok {
		rec = &Record{
			Hash:        hash,
			ByteLength:  int64(len(data)),
			TimeRange:   tr,
			KeyframeHit: keyframe,
		}
		h.records[hash] = rec
		h.order = append(h.order, hash)
	} else {
		// Same bytes must report the same length; a mismatch signals a
		// collision in the caller, not in SHA-256.
		if rec.ByteLength != int64(len(data)) {
			return nil, errs.New(errs.KindSegment, ref.Source, ref.ByteRange.Start,
				ref.Segment, ref.TrackID,
				"hash collision with mismatched length %d vs %d",
				rec.ByteLength, len(data))
		}
		rec.KeyframeHit = rec.KeyframeHit || keyframe
	}
	rec.Refs = append(rec.Refs, ref)
	return rec, nil
}

// Records returns all fingerprint records in deterministic (first-seen
// hash) order, with each record's references sorted for stable output.
func (h *Hasher) Records() []Record {
	out := make([]Record, 0, len(h.order))
	for _, hash := range h.order {
		rec := h.records[hash]
		refs := append([]SourceRef(nil), rec.Refs...)
		sort.SliceStable(refs, func(i, j int) bool {
			if refs[i].Source != refs[j].Source {
				return refs[i].Source < refs[j].Source
			}
			if refs[i].Segment != refs[j].Segment {
				return refs[i].Segment < refs[j].Segment
			}
			return refs[i].ByteRange.Start < refs[j].ByteRange.Start
		})
		cp := *rec
		cp.Refs = refs
		out = append(out, cp)
	}
	return out
}

// Lookup returns the record for a content hash.
func (h *Hasher) Lookup(hash string) (*Record, bool) {
	rec, ok := h.records[hash]
	return rec, ok
}

// Len reports the number of distinct content objects (post-dedup).
func (h *Hasher) Len() int { return len(h.records) }

// RefCount totals every registered source reference (pre-dedup).
func (h *Hasher) RefCount() int {
	n := 0
	for _, rec := range h.records {
		n += len(rec.Refs)
	}
	return n
}

// ManifestHash hashes the manifest bytes (MPD or playlist) for checkpoint
// identity and repeat-run equality checks.
func ManifestHash(raw []byte) string { return Sum(raw) }

// String renders a compact record for terminal summaries.
func (r Record) String() string {
	return fmt.Sprintf("sha256:%s bytes=%d refs=%d keyframe=%t",
		r.Hash[:12], r.ByteLength, len(r.Refs), r.KeyframeHit)
}
