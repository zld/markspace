// Package index builds per-track keyframe lookups used by the segmenter and the
// independent verifier. Entries retain source spans so an index hit can be
// checked against the real bytes it names.
package index

import (
	"sort"

	"mediad/internal/ebml"
)

// Kind distinguishes how an entry may be used as a cut point.
type Kind string

const (
	KindVideoKeyframe Kind = "video_keyframe"
	KindAudio         Kind = "audio"
)

// Entry is one indexed block.
type Entry struct {
	Track    uint64    `json:"track"`
	Kind     Kind      `json:"kind"`
	TimeNS   int64     `json:"time_ns"`
	Source   ebml.Span `json:"source"`
	Timecode int64     `json:"timecode"`
}

// Index holds sorted entries per track.
type Index struct {
	byTrack map[uint64][]Entry
	types   map[uint64]ebml.TrackType
}

// New builds an empty index for the given tracks.
func New(tracks []ebml.Track) *Index {
	ix := &Index{byTrack: make(map[uint64][]Entry, len(tracks)), types: make(map[uint64]ebml.TrackType, len(tracks))}
	for _, t := range tracks {
		ix.types[t.Number] = t.Type
	}
	return ix
}

// Add records one block. Audio entries are appended for every block so the
// segmenter can assign audio by time interval; video entries only for
// keyframes, which are the legal cut points.
func (ix *Index) Add(track ebml.TrackType, block ebml.Block, nanos int64) {
	switch track {
	case ebml.TrackVideo:
		if !block.Keyframe {
			return
		}
		ix.byTrack[block.Track] = append(ix.byTrack[block.Track], Entry{
			Track: block.Track, Kind: KindVideoKeyframe, TimeNS: nanos, Source: block.Source, Timecode: block.Timecode,
		})
	case ebml.TrackAudio:
		ix.byTrack[block.Track] = append(ix.byTrack[block.Track], Entry{
			Track: block.Track, Kind: KindAudio, TimeNS: nanos, Source: block.Source, Timecode: block.Timecode,
		})
	}
}

// Finalize sorts every track's entries by time and source offset. Sorting is
// stable so equal timestamps keep document order and runs stay deterministic.
func (ix *Index) Finalize() {
	for _, entries := range ix.byTrack {
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].TimeNS != entries[j].TimeNS {
				return entries[i].TimeNS < entries[j].TimeNS
			}
			return entries[i].Source.Offset < entries[j].Source.Offset
		})
	}
}

// Entries returns a copy of a track's entries.
func (ix *Index) Entries(track uint64) []Entry {
	src := ix.byTrack[track]
	out := make([]Entry, len(src))
	copy(out, src)
	return out
}

// Tracks returns track numbers that have entries, sorted ascending.
func (ix *Index) Tracks() []uint64 {
	out := make([]uint64, 0, len(ix.byTrack))
	for t := range ix.byTrack {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// VideoTrack returns the lowest-numbered video track, if any.
func (ix *Index) VideoTrack() (uint64, bool) {
	var found uint64
	ok := false
	for track, typ := range ix.types {
		if typ != ebml.TrackVideo {
			continue
		}
		if !ok || track < found {
			found, ok = track, true
		}
	}
	return found, ok
}

// AtOrBefore finds the last sticky keyframe at or before nanos on track,
// returning its entry and the distance in nanoseconds. ok is false when the
// track has no keyframe that early.
func (ix *Index) AtOrBefore(track uint64, nanos int64) (Entry, int64, bool) {
	entries := ix.byTrack[track]
	i := sort.Search(len(entries), func(i int) bool { return entries[i].TimeNS > nanos })
	if i == 0 {
		return Entry{}, 0, false
	}
	e := entries[i-1]
	return e, nanos - e.TimeNS, true
}

// Nearest finds the keyframe closest to nanos, preferring the earlier one on
// ties so cuts do not drift forwards.
func (ix *Index) Nearest(track uint64, nanos int64) (Entry, int64, bool) {
	entries := ix.byTrack[track]
	if len(entries) == 0 {
		return Entry{}, 0, false
	}
	i := sort.Search(len(entries), func(i int) bool { return entries[i].TimeNS >= nanos })
	switch {
	case i == 0:
		return entries[0], entries[0].TimeNS - nanos, true
	case i == len(entries):
		e := entries[len(entries)-1]
		return e, nanos - e.TimeNS, true
	}
	after, before := entries[i], entries[i-1]
	if nanos-before.TimeNS <= after.TimeNS-nanos {
		return before, nanos - before.TimeNS, true
	}
	return after, after.TimeNS - nanos, true
}

// HitReport summarizes how well an index covers a cut grid.
type HitReport struct {
	VideoTrack uint64  `json:"video_track"`
	Cuts       int     `json:"cuts"`
	Hits       int     `json:"hits"`
	HitRate    float64 `json:"hit_rate"`
	MaxDeltaNS int64   `json:"max_delta_ns"`
}

// Hits reports whether each cut coincides with a real keyframe within
// tolerance on the video track. A cut with no keyframe within tolerance is a
// miss; the worst observed delta is reported either way.
func (ix *Index) Hits(track uint64, cuts []int64, tolerance int64) HitReport {
	rep := HitReport{VideoTrack: track, Cuts: len(cuts)}
	for _, cut := range cuts {
		e, delta, ok := ix.Nearest(track, cut)
		if !ok {
			continue
		}
		if delta > rep.MaxDeltaNS {
			rep.MaxDeltaNS = delta
		}
		// The nearest entry must both be within tolerance and actually be a
		// video keyframe, not an audio entry misindexed under this track.
		if delta <= tolerance && e.Kind == KindVideoKeyframe {
			rep.Hits++
		}
	}
	if rep.Cuts > 0 {
		rep.HitRate = float64(rep.Hits) / float64(rep.Cuts)
	}
	return rep
}

// CutWithin reports whether a keyframe exists for every given video track
// within tolerance of cut. It is used by the verifier to cross-check the
// manifest's claimed keyframe positions against the index.
func (ix *Index) CutWithin(tracks []uint64, cut, tolerance int64) bool {
	for _, t := range tracks {
		if _, delta, ok := ix.Nearest(t, cut); !ok || delta > tolerance {
			return false
		}
	}
	return true
}
