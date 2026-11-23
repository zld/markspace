// Package index builds the random-access keyframe index and persists it
// in fixed-size chunks.
package index

import (
	"mediapack/internal/clock"
)

// Entry is one repaired fragment record. Entries with Keyframe set are
// members of the random-access index; chunks persist all repaired records
// so that a run can be resumed from them.
type Entry struct {
	Track     uint32   `json:"track"`
	Logical   uint32   `json:"logical"`
	Mfhd      uint32   `json:"mfhd"`
	Time      int64    `json:"time"`
	Duration  int64    `json:"duration"`
	Timescale uint32   `json:"timescale"`
	Source    string   `json:"source"`
	Offset    int64    `json:"offset"`
	Size      int64    `json:"size"`
	Keyframe  bool     `json:"keyframe"`
	Repaired  bool     `json:"repaired,omitempty"`
	Hash      [32]byte `json:"hash"`
}

// End returns the decode end tick of the entry.
func (e Entry) End() int64 { return e.Time + e.Duration }

// Index holds keyframe entries in insertion order, which is monotonic per
// track by construction.
type Index struct {
	entries []Entry
	last    map[uint32]int64
}

// New returns an empty index.
func New() *Index { return &Index{last: map[uint32]int64{}} }

// Add appends a keyframe entry, enforcing per-track monotonicity. Only
// repaired fragments that passed timeline validation may be added.
func (ix *Index) Add(e Entry) error {
	if !e.Keyframe {
		return &clock.Error{Cat: clock.CatInvariant, Track: e.Track,
			Fragment: int64(e.Logical), Msg: "non-keyframe entry refused by index"}
	}
	if last, ok := ix.last[e.Track]; ok && e.Time < last {
		return &clock.Error{Cat: clock.CatInvariant, Track: e.Track,
			Fragment: int64(e.Logical), Msg: "index entry not monotonic"}
	}
	ix.last[e.Track] = e.Time
	ix.entries = append(ix.entries, e)
	return nil
}

// Entries returns a copy of all entries in insertion order.
func (ix *Index) Entries() []Entry {
	out := make([]Entry, len(ix.entries))
	copy(out, ix.entries)
	return out
}

// Len returns the number of keyframe entries.
func (ix *Index) Len() int { return len(ix.entries) }

// Query returns the latest keyframe of track at or before ticks. The
// second result is false when the track has no keyframe at or before the
// requested time, including tracks without any keyframe.
func (ix *Index) Query(track uint32, ticks int64) (Entry, bool) {
	var best Entry
	found := false
	for _, e := range ix.entries {
		if e.Track != track {
			continue
		}
		if e.Time > ticks {
			break // per-track entries are monotonic
		}
		best, found = e, true
	}
	return best, found
}

// QuerySeconds converts t seconds into track ticks rounding down (so the
// result never overshoots the requested instant) and queries the index.
func (ix *Index) QuerySeconds(track uint32, t clock.Rational, timescale uint32) (Entry, bool) {
	ticks := clock.MulInt64(t, int64(timescale), clock.RoundDown)
	return ix.Query(track, ticks)
}

// First returns the earliest keyframe of track.
func (ix *Index) First(track uint32) (Entry, bool) {
	for _, e := range ix.entries {
		if e.Track == track {
			return e, true
		}
	}
	return Entry{}, false
}

// Last returns the latest keyframe of track.
func (ix *Index) Last(track uint32) (Entry, bool) {
	for i := len(ix.entries) - 1; i >= 0; i-- {
		if ix.entries[i].Track == track {
			return ix.entries[i], true
		}
	}
	return Entry{}, false
}
