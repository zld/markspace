// Package index builds a queryable key frame index. Only checksum-valid, fully
// parsed frames are admitted, so a damaged frame can never be returned as a
// seek target. Every entry keeps its original input offset for traceability.
package index

import (
	"sort"

	"medcheck/internal/model"
)

// Entry is one indexed key frame.
type Entry struct {
	Track  uint32
	Source uint32
	Seq    uint32
	Micros int64
	Ticks  int64
	Base   model.Rational
	Offset model.RawOffset
}

// KeyframeIndex is an immutable-at-query-time per-track sorted index.
type KeyframeIndex struct {
	entries []Entry
}

// Build constructs an index from frames. Invalid, error-bearing or non-key
// frames are excluded by construction.
func Build(frames []*model.Frame) *KeyframeIndex {
	idx := &KeyframeIndex{}
	for _, f := range frames {
		if f == nil || !f.Valid || f.Err != nil {
			continue
		}
		if f.Type != model.TypeKeyFrame {
			continue
		}
		idx.entries = append(idx.entries, Entry{
			Track:  f.Track,
			Source: f.Source,
			Seq:    f.Seq,
			Micros: f.EventTime.MicrosOr(f.EventTime.Ticks),
			Ticks:  f.EventTime.Ticks,
			Base:   f.EventTime.Base,
			Offset: f.Offset,
		})
	}
	sort.SliceStable(idx.entries, func(i, j int) bool {
		if idx.entries[i].Track != idx.entries[j].Track {
			return idx.entries[i].Track < idx.entries[j].Track
		}
		if idx.entries[i].Micros != idx.entries[j].Micros {
			return idx.entries[i].Micros < idx.entries[j].Micros
		}
		return idx.entries[i].Offset.Start < idx.entries[j].Offset.Start
	})
	return idx
}

// Len returns the number of indexed key frames.
func (k *KeyframeIndex) Len() int { return len(k.entries) }

// Entries returns a defensive copy of all entries in deterministic order.
func (k *KeyframeIndex) Entries() []Entry {
	out := make([]Entry, len(k.entries))
	copy(out, k.entries)
	return out
}

// AtOrBefore returns the latest key frame on track whose event time is at or
// before micros. The boolean is false when no such frame exists.
func (k *KeyframeIndex) AtOrBefore(track uint32, micros int64) (Entry, bool) {
	best := -1
	for i, e := range k.entries {
		if e.Track != track {
			continue
		}
		if e.Micros <= micros {
			best = i
		} else {
			break
		}
	}
	if best < 0 {
		return Entry{}, false
	}
	return k.entries[best], true
}

// Exact returns the key frame on track at the given event time, if indexed.
func (k *KeyframeIndex) Exact(track uint32, micros int64) (Entry, bool) {
	for _, e := range k.entries {
		if e.Track == track && e.Micros == micros {
			return e, true
		}
	}
	return Entry{}, false
}

// Interval is the gap between two consecutive key frames on one track.
type Interval struct {
	Track      uint32
	FromMicros int64
	ToMicros   int64
	GapMicros  int64
}

// Intervals returns consecutive key frame gaps per track in time order.
func (k *KeyframeIndex) Intervals() []Interval {
	var out []Interval
	var prev *Entry
	for i := range k.entries {
		cur := &k.entries[i]
		if prev != nil && prev.Track == cur.Track {
			out = append(out, Interval{
				Track:      cur.Track,
				FromMicros: prev.Micros,
				ToMicros:   cur.Micros,
				GapMicros:  cur.Micros - prev.Micros,
			})
		}
		prev = cur
	}
	return out
}
