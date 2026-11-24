// Package segment splits a unified sample timeline at requestable maximum
// durations while preserving every sample's origin.
package segment

import (
	"sort"

	"mediamerge/internal/model"
)

// SourceFrag identifies one input fragment that contributed samples to a
// segment.
type SourceFrag struct {
	File string `json:"file"`
	Seq  uint32 `json:"seq"`
}

// Segment is one output interval. Samples never straddle a boundary: every
// sample is wholly inside exactly one segment.
type Segment struct {
	Index          int              `json:"index"`
	Time           model.Interval   `json:"time"`
	Tracks         []uint32         `json:"tracks"`
	Samples        []model.Sample   `json:"samples"`
	Sources        []SourceFrag     `json:"sources"`
	RequestedEndNs model.NsTime     `json:"requested_end_ns"`
	BoundaryShift  model.NsDuration `json:"boundary_shift_ns"`
}

// Len returns the actual segment duration.
func (s Segment) Len() model.NsDuration { return s.Time.Len() }

// ShiftMs returns the boundary adjustment in milliseconds (<= 0 when the
// boundary moved earlier than requested).
func (s Segment) ShiftMs() float64 {
	return float64(s.BoundaryShift) / 1e6
}

// Options controls segmentation.
type Options struct {
	MaxDuration model.NsDuration
}

// Result carries segments plus the alignment statistics needed by the
// manifest.
type Result struct {
	Segments []Segment `json:"segments"`
	// MaxShiftNs is the largest backward boundary adjustment observed.
	MaxShiftNs model.NsDuration `json:"max_shift_ns"`
}

// Split cuts samples into segments of at most opts.MaxDuration. The ideal
// grid is anchored at the earliest sample start; if the grid point falls
// between samples the boundary moves backwards to the nearest legal
// position (one where no sample straddles the cut). When a single sample is
// longer than the limit the nearest legal later boundary is used so the
// split always makes progress.
//
// Samples must be non-overlapping within each track. The input slice is not
// modified.
func Split(samples []model.Sample, opts Options) Result {
	var res Result
	if len(samples) == 0 || opts.MaxDuration <= 0 {
		return res
	}
	ordered := append([]model.Sample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.Key() < b.Key()
	})

	start := ordered[0].Start
	for _, s := range ordered {
		if s.Start < start {
			start = s.Start
		}
	}
	boundaries := legalBoundaries(ordered)
	contentEnd := boundaries[len(boundaries)-1]

	cursor := start
	idx := 0
	for cursor < contentEnd {
		target := cursor + model.NsTime(opts.MaxDuration)
		// A target at or past the content end is a natural tail, not a
		// misaligned boundary.
		if target >= contentEnd {
			target = contentEnd
		}
		cut := lastBoundaryAtOrBefore(boundaries, target)
		shift := model.NsDuration(0)
		switch {
		case cut > cursor:
			shift = model.NsDuration(cut - target)
		default:
			// No legal boundary in (cursor, target]: take the next one.
			cut = firstBoundaryAfter(boundaries, cursor)
			shift = model.NsDuration(cut - target)
		}
		if cut <= cursor {
			break // cannot make progress; malformed input
		}
		seg := Segment{
			Index:          idx,
			Time:           model.Interval{Start: cursor, End: cut},
			RequestedEndNs: target,
			BoundaryShift:  shift,
		}
		if -shift > res.MaxShiftNs {
			res.MaxShiftNs = -shift
		}
		seg.Samples, seg.Tracks, seg.Sources = collect(ordered, cursor, cut)
		res.Segments = append(res.Segments, seg)
		cursor = cut
		idx++
	}
	return res
}

// legalBoundaries returns every sample start/end time at which no sample
// straddles the cut, in ascending order.
func legalBoundaries(samples []model.Sample) []model.NsTime {
	cand := make(map[model.NsTime]bool)
	for _, s := range samples {
		cand[s.Start] = true
		cand[s.End()] = true
	}
	points := make([]model.NsTime, 0, len(cand))
	for t := range cand {
		points = append(points, t)
	}
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })
	out := points[:0]
	for _, t := range points {
		if !straddled(samples, t) {
			out = append(out, t)
		}
	}
	return out
}

func straddled(samples []model.Sample, t model.NsTime) bool {
	for _, s := range samples {
		if s.Start < t && t < s.End() {
			return true
		}
	}
	return false
}

func lastBoundaryAtOrBefore(bounds []model.NsTime, t model.NsTime) model.NsTime {
	i := sort.Search(len(bounds), func(i int) bool { return bounds[i] > t })
	if i == 0 {
		return bounds[0]
	}
	return bounds[i-1]
}

func firstBoundaryAfter(bounds []model.NsTime, t model.NsTime) model.NsTime {
	i := sort.Search(len(bounds), func(i int) bool { return bounds[i] > t })
	if i >= len(bounds) {
		return bounds[len(bounds)-1]
	}
	return bounds[i]
}

// collect gathers the samples starting in [lo, hi) plus the tracks and
// source fragments they came from, in deterministic order.
func collect(samples []model.Sample, lo, hi model.NsTime) ([]model.Sample, []uint32, []SourceFrag) {
	var seg []model.Sample
	trackSet := make(map[uint32]bool)
	srcSet := make(map[SourceFrag]bool)
	for _, s := range samples {
		if s.Start < lo || s.Start >= hi {
			continue
		}
		seg = append(seg, s)
		trackSet[s.TrackID] = true
		srcSet[SourceFrag{File: s.SourceFile, Seq: s.SeqNum}] = true
	}
	sort.SliceStable(seg, func(i, j int) bool { return seg[i].Key() < seg[j].Key() })
	tracks := make([]uint32, 0, len(trackSet))
	for id := range trackSet {
		tracks = append(tracks, id)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })
	sources := make([]SourceFrag, 0, len(srcSet))
	for src := range srcSet {
		sources = append(sources, src)
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].File != sources[j].File {
			return sources[i].File < sources[j].File
		}
		return sources[i].Seq < sources[j].Seq
	})
	return seg, tracks, sources
}
