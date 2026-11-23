// Package recover rebuilds usable indexes from damaged containers and
// performs bounded, deterministic RTP packet recovery.
package recover

import (
	"sort"

	"archivectl/internal/ebml"
)

// IndexEntry maps one recovered block's timecode to its source offset.
type IndexEntry struct {
	TrackNumber uint32
	TimeNs      int64
	Offset      int64
	Size        int
	Keyframe    bool
	// Complete is false for entries adjacent to a truncation boundary;
	// damaged offsets are never emitted as valid samples.
	Complete bool
}

// Index is the rebuilt seek index.
type Index struct {
	Entries      []IndexEntry
	Truncated    bool
	DamagedBytes []int64 // offsets reported as parse errors
	Rebuilt      bool
}

// MaxRecoveryWindow caps the RTP reorder/loss window in packets.
const MaxRecoveryWindow = 256

// RebuildIndex constructs an index from a parsed EBML document. Complete
// blocks become entries; every parse error offset is recorded separately so
// a damaged position can never be mistaken for a valid sample.
func RebuildIndex(doc *ebml.Document) *Index {
	idx := &Index{Rebuilt: true}
	for _, c := range doc.Clusters {
		if c.Truncated {
			idx.Truncated = true
		}
		for _, b := range c.Blocks {
			idx.Entries = append(idx.Entries, IndexEntry{
				TrackNumber: b.TrackNumber,
				TimeNs:      b.TimecodeNs,
				Offset:      b.Offset,
				Size:        len(b.Data),
				Keyframe:    b.Keyframe,
				Complete:    true,
			})
		}
		for _, e := range c.Errors {
			idx.DamagedBytes = append(idx.DamagedBytes, e.Offset)
		}
	}
	for _, e := range doc.Errors {
		idx.DamagedBytes = append(idx.DamagedBytes, e.Offset)
		if e.Category == ebml.CategoryTruncated ||
			e.Category == ebml.CategoryOutOfRange {
			idx.Truncated = true
		}
	}
	sort.Slice(idx.Entries, func(i, j int) bool {
		if idx.Entries[i].TimeNs != idx.Entries[j].TimeNs {
			return idx.Entries[i].TimeNs < idx.Entries[j].TimeNs
		}
		return idx.Entries[i].Offset < idx.Entries[j].Offset
	})
	sort.Slice(idx.DamagedBytes, func(i, j int) bool {
		return idx.DamagedBytes[i] < idx.DamagedBytes[j]
	})
	return idx
}

// OffsetHealthy reports whether off is free of any recorded damaged region.
func (ix *Index) OffsetHealthy(off int64) bool {
	for _, d := range ix.DamagedBytes {
		if d == off {
			return false
		}
	}
	return true
}

// Count returns the number of usable index entries.
func (ix *Index) Count() int { return len(ix.Entries) }
