// Package index builds a queryable keyframe index over parsed media
// segments. Entries carry the original segment name, sample ordinal and
// byte offset, and keyframe intervals are measured from actual sample
// times so variable-frame-rate material is handled without a fixed-rate
// assumption. Entries depending on a damaged region can be quarantined
// while independent segments stay queryable.
package index

import (
	"sort"
	"sync"
)

// Entry is one indexed keyframe.
type Entry struct {
	Segment     string  `json:"segment"`
	MediaSeq    uint64  `json:"media_seq"`
	SampleIndex int     `json:"sample_index"`
	ByteOffset  int64   `json:"byte_offset"`
	TimeSeconds float64 `json:"time_seconds"`
	// IntervalSeconds is the measured gap from the previous keyframe;
	// zero for the first entry. Derived from real sample timestamps.
	IntervalSeconds float64 `json:"interval_seconds"`
	// Quarantined marks entries whose backing region failed validation.
	Quarantined bool `json:"quarantined"`
}

// Index is an ordered, concurrency-safe keyframe collection.
type Index struct {
	mu      sync.RWMutex
	entries []Entry
}

// Add appends one entry; interval is computed by the builder.
func (idx *Index) Add(e Entry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries = append(idx.entries, e)
}

// Snapshot returns a copy of the current entries in timeline order.
func (idx *Index) Snapshot() []Entry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]Entry, len(idx.entries))
	copy(out, idx.entries)
	return out
}

// Len reports the total entry count including quarantined ones.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

// Available counts entries still safe to query.
func (idx *Index) Available() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	n := 0
	for _, e := range idx.entries {
		if !e.Quarantined {
			n++
		}
	}
	return n
}

// Lookup returns the keyframe entry for the given segment and sample
// ordinal. Quarantined entries and misses report ok=false.
func (idx *Index) Lookup(segment string, sampleIndex int) (Entry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for _, e := range idx.entries {
		if e.Segment == segment && e.SampleIndex == sampleIndex {
			if e.Quarantined {
				return Entry{}, false
			}
			return e, true
		}
	}
	return Entry{}, false
}

// LookupAtOrBefore returns the latest non-quarantined keyframe whose time
// is at or before t, together with its position.
func (idx *Index) LookupAtOrBefore(t float64) (Entry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	pos := sort.Search(len(idx.entries), func(i int) bool {
		return idx.entries[i].TimeSeconds > t
	}) - 1
	for pos >= 0 {
		if !idx.entries[pos].Quarantined {
			return idx.entries[pos], true
		}
		pos--
	}
	return Entry{}, false
}

// Quarantine invalidates every entry backed by the named segment and
// returns how many were isolated.
func (idx *Index) Quarantine(segment string) int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	n := 0
	for i := range idx.entries {
		if idx.entries[i].Segment == segment && !idx.entries[i].Quarantined {
			idx.entries[i].Quarantined = true
			n++
		}
	}
	return n
}

// HitRate runs the supplied queries against the index and returns the
// fraction (0..1) that resolved to live entries.
func (idx *Index) HitRate(queries []Query) float64 {
	if len(queries) == 0 {
		return 0
	}
	hits := 0
	for _, q := range queries {
		if _, ok := idx.Lookup(q.Segment, q.SampleIndex); ok {
			hits++
		}
	}
	return float64(hits) / float64(len(queries))
}

// Query identifies one keyframe lookup target.
type Query struct {
	Segment     string
	SampleIndex int
}

// SampleView is the minimal per-sample information the builder needs;
// pipeline code adapts stream.Sample onto it.
type SampleView struct {
	SampleIndex int
	Keyframe    bool
	ByteOffset  int64
	TimeSeconds float64
}

// Build appends keyframe entries for one segment, computing intervals
// from actual sample times. The previous keyframe time (or 0 for the
// first segment) threads VFR-correct spacing across segment boundaries.
func Build(idx *Index, segment string, mediaSeq uint64, samples []SampleView, prevKeyTime float64, havePrev bool) float64 {
	lastKey := prevKeyTime
	have := havePrev
	for _, s := range samples {
		if !s.Keyframe {
			continue
		}
		e := Entry{
			Segment: segment, MediaSeq: mediaSeq,
			SampleIndex: s.SampleIndex, ByteOffset: s.ByteOffset,
			TimeSeconds: s.TimeSeconds,
		}
		if have {
			e.IntervalSeconds = s.TimeSeconds - lastKey
		}
		idx.Add(e)
		lastKey = s.TimeSeconds
		have = true
	}
	if have {
		return lastKey
	}
	return prevKeyTime
}
