// Package index builds a random-access index over scanned container tracks.
// Entries are sorted by (track, time) so any time range can be served with a
// binary search instead of a linear scan.
package index

import (
	"sort"

	"mediakernel/internal/clock"
	"mediakernel/internal/mp4scan"
)

// Entry is one indexed sample.
type Entry struct {
	TrackID     uint32
	SampleIndex uint32
	Start       clock.Rational // sample start, seconds
	End         clock.Rational // sample end (start + duration), seconds
	DTS         int64          // decode timestamp in track ticks
	Offset      int64          // absolute file offset of the sample payload
	Size        uint32
	Key         bool
}

// Index is the sorted random-access structure over all tracks.
type Index struct {
	Entries []Entry // sorted by (TrackID, Start, SampleIndex)
	byTrack map[uint32][]Entry
}

// Build converts a scanned container into an index. Sample durations come
// from the distance to the next sample; the final sample inherits the
// previous duration (zero for single-sample tracks).
func Build(c *mp4scan.Container) (*Index, error) {
	ix := &Index{byTrack: map[uint32][]Entry{}}
	for _, tr := range c.Tracks {
		var prevDur int64
		for i, s := range tr.Samples {
			var dur int64
			if i+1 < len(tr.Samples) {
				dur = tr.Samples[i+1].DTS - s.DTS
			} else {
				dur = prevDur
			}
			prevDur = dur
			start, err := clock.TicksToSeconds(s.DTS, tr.Timescale)
			if err != nil {
				return nil, err
			}
			delta, err := clock.TicksToSeconds(dur, tr.Timescale)
			if err != nil {
				return nil, err
			}
			end, err := clock.Add(start, delta)
			if err != nil {
				return nil, err
			}
			e := Entry{
				TrackID: tr.ID, SampleIndex: s.Index,
				Start: start, End: end, DTS: s.DTS,
				Offset: s.Offset, Size: s.Size, Key: s.Key,
			}
			ix.Entries = append(ix.Entries, e)
			ix.byTrack[tr.ID] = append(ix.byTrack[tr.ID], e)
		}
	}
	sort.Slice(ix.Entries, func(i, j int) bool {
		a, b := ix.Entries[i], ix.Entries[j]
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if c := clock.Cmp(a.Start, b.Start); c != 0 {
			return c < 0
		}
		return a.SampleIndex < b.SampleIndex
	})
	return ix, nil
}

// Track returns the time-sorted entries of one track, or nil.
func (ix *Index) Track(id uint32) []Entry { return ix.byTrack[id] }

// Tracks returns the sorted list of indexed track ids.
func (ix *Index) Tracks() []uint32 {
	out := make([]uint32, 0, len(ix.byTrack))
	for id := range ix.byTrack {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Len returns the total number of index entries.
func (ix *Index) Len() int { return len(ix.Entries) }
