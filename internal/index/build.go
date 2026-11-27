// Package index rebuilds random-access points from repaired, indexable
// keyframe samples and answers time-targeted queries with explicit hit/miss
// accounting.
package index

import (
	"sort"

	"tsrepair/internal/segment"
)

// Builder accumulates random-access entries for one representation. Only
// samples that are complete, indexable keyframes are admitted, so a caller
// must feed boundary-checked data.
type Builder struct {
	entries []segment.IndexEntry
	pid     uint16
}

// NewBuilder creates a builder for keyframes on the given PID.
func NewBuilder(keyframePID uint16) *Builder {
	return &Builder{pid: keyframePID}
}

// AddSample admits one sample. The sample must already be repaired and
// boundary-checked; non-keyframe or unindexable samples are ignored. endNanos
// is the start time of the following sample (or the known stream end for the
// final one), defining the half-open coverage range.
func (b *Builder) AddSample(sampleIndex int, keyframe, indexable bool, startNanos, endNanos, byteOffset int64) {
	if !keyframe || !indexable {
		return
	}
	b.entries = append(b.entries, segment.IndexEntry{
		SampleIndex: sampleIndex,
		KeyframePID: b.pid,
		StartNanos:  startNanos,
		EndNanos:    endNanos,
		ByteOffset:  byteOffset,
	})
}

// Build returns the entries sorted by start time. Sorting makes the output
// deterministic even if samples were added out of order.
func (b *Builder) Build() []segment.IndexEntry {
	out := make([]segment.IndexEntry, len(b.entries))
	copy(out, b.entries)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartNanos != out[j].StartNanos {
			return out[i].StartNanos < out[j].StartNanos
		}
		return out[i].SampleIndex < out[j].SampleIndex
	})
	return out
}

// Stats summarises query outcomes.
type Stats struct {
	Hits   int
	Misses int
}

// HitRate returns hits/(hits+misses), or 1.0 when no queries were made.
func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 1.0
	}
	return float64(s.Hits) / float64(total)
}

// Index is an immutable, queryable random-access index.
type Index struct {
	entries []segment.IndexEntry
	stats   Stats
}

// NewIndex wraps sorted entries. Entries are copied and re-sorted.
func NewIndex(entries []segment.IndexEntry) *Index {
	sorted := make([]segment.IndexEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].StartNanos < sorted[j].StartNanos
	})
	return &Index{entries: sorted}
}

// Lookup returns the entry whose half-open range [StartNanos, EndNanos)
// contains targetNanos. A hit is defined strictly by range containment; a
// target before the first entry or after the last range end is a miss and is
// counted, never falsely reported as a hit.
func (idx *Index) Lookup(targetNanos int64) (segment.IndexEntry, bool) {
	// Binary search for the last entry with StartNanos <= target.
	pos := sort.Search(len(idx.entries), func(i int) bool {
		return idx.entries[i].StartNanos > targetNanos
	}) - 1
	if pos < 0 {
		idx.stats.Misses++
		return segment.IndexEntry{}, false
	}
	e := idx.entries[pos]
	if targetNanos < e.StartNanos || targetNanos >= e.EndNanos {
		idx.stats.Misses++
		return segment.IndexEntry{}, false
	}
	idx.stats.Hits++
	return e, true
}

// Stats returns a copy of the accumulated query statistics.
func (idx *Index) Stats() Stats { return idx.stats }

// Entries returns the sorted index entries.
func (idx *Index) Entries() []segment.IndexEntry { return idx.entries }
