// Package index builds a local, random-access index over repaired tracks and
// persists it in checksummed chunks. The in-memory index answers time-based
// lookups; the on-disk chunk format is the unit of crash recovery.
package index

import (
	"fmt"
	"sort"
	"sync"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/repair"
)

// Entry is one random-access row. Only sync samples are seek targets, but the
// index stores the surrounding rows so coverage gaps are detectable.
type Entry struct {
	Track      uint32
	Time       clock.Rational
	FileOffset uint64
	Size       uint32
	Sync       bool
	Sample     uint32
}

// Index is the materialized, sorted set of entries for one processed input.
type Index struct {
	mu      sync.RWMutex
	tracks  map[uint32][]Entry // entries kept sorted by Time per track
	coverage map[uint32]span   // min/max time present on each track
}

type span struct {
	min clock.Rational
	max clock.Rational
}

// New returns an empty index.
func New() *Index {
	return &Index{tracks: map[uint32][]Entry{}, coverage: map[uint32]span{}}
}

// Add inserts entries. Callers normally feed whole chunks; Add keeps each
// track's slice sorted so a partially built index stays queryable.
func (ix *Index) Add(entries []Entry) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, e := range entries {
		ix.tracks[e.Track] = append(ix.tracks[e.Track], e)
	}
	for track, list := range ix.tracks {
		sort.Slice(list, func(a, b int) bool {
			c := list[a].Time.Cmp(list[b].Time)
			if c == 0 {
				return list[a].Sample < list[b].Sample
			}
			return c < 0
		})
		ix.tracks[track] = list
		if len(list) > 0 {
			ix.coverage[track] = span{min: list[0].Time, max: list[len(list)-1].Time}
		}
	}
}

// EntryCount is the total number of indexed rows across all tracks.
func (ix *Index) EntryCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n := 0
	for _, list := range ix.tracks {
		n += len(list)
	}
	return n
}

// SyncCount returns how many entries are random-access points.
func (ix *Index) SyncCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n := 0
	for _, list := range ix.tracks {
		for _, e := range list {
			if e.Sync {
				n++
			}
		}
	}
	return n
}

// Lookup returns the sync sample that contains target: the latest sync entry
// at or before target on the track. A target outside the track's coverage or a
// gap with no preceding sync returns an ErrGap.
func (ix *Index) Lookup(track uint32, target clock.Rational) (Entry, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	list, ok := ix.tracks[track]
	if !ok || len(list) == 0 {
		return Entry{}, fmt.Errorf("%w: track %d has no index", clock.ErrGap, track)
	}
	cov := ix.coverage[track]
	if target.Cmp(cov.min) < 0 || target.Cmp(cov.max) > 0 {
		return Entry{}, fmt.Errorf("%w: target %s outside [%s,%s] on track %d",
			clock.ErrGap, target, cov.min, cov.max, track)
	}
	// Binary search for the last sync at or before target.
	idx := sort.Search(len(list), func(i int) bool {
		return list[i].Time.Cmp(target) > 0
	}) - 1
	for idx >= 0 && !list[idx].Sync {
		idx--
	}
	if idx < 0 {
		return Entry{}, fmt.Errorf("%w: no sync sample at or before %s on track %d",
			clock.ErrGap, target, track)
	}
	return list[idx], nil
}

// Entries returns a defensive copy of one track's sorted entries.
func (ix *Index) Entries(track uint32) []Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	src := ix.tracks[track]
	out := make([]Entry, len(src))
	copy(out, src)
	return out
}

// Tracks lists the track IDs currently indexed.
func (ix *Index) Tracks() []uint32 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]uint32, 0, len(ix.tracks))
	for t := range ix.tracks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FromRepair converts repaired tracks into index entries, preserving the
// on-disk locations and sync flags carried through repair.
func FromRepair(rt *repair.RepairedTrack) []Entry {
	out := make([]Entry, len(rt.Samples))
	for i, s := range rt.Samples {
		loc := rt.Locations[i]
		out[i] = Entry{
			Track:      rt.ID,
			Time:       s.Value,
			FileOffset: loc.FileOffset,
			Size:       loc.Size,
			Sync:       loc.Sync,
			Sample:     uint32(i),
		}
	}
	return out
}
