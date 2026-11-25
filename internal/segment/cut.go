// Package segment creates half-open story segments and preserves transition
// overlaps as multiple source-track mappings.
package segment

import (
	"sort"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/trace"
)

// Cut is an editorial cut point. It belongs to the interval to its right.
type Cut struct {
	At    model.NS `json:"at_ns"`
	Label string   `json:"label,omitempty"`
}

// Transition describes an overlap between two source tracks. [Start, End) is
// the overlap on the unified timeline.
type Transition struct {
	ID        string   `json:"id"`
	Start     model.NS `json:"start_ns"`
	End       model.NS `json:"end_ns"`
	FromTrack int      `json:"from_track"`
	ToTrack   int      `json:"to_track"`
}

// Params are deterministic segmentation parameters and part of cache keys.
type Params struct {
	Cuts        []Cut        `json:"cuts"`
	Transitions []Transition `json:"transitions,omitempty"`
}

// SourceRef maps one output interval to one source track interval and bytes.
type SourceRef struct {
	TrackID       int             `json:"track_id"`
	Timeline      model.Interval  `json:"timeline"`
	Local         model.Interval  `json:"local_ns"`
	TickStart     uint64          `json:"tick_start"`
	TickEnd       uint64          `json:"tick_end"`
	ByteRange     model.ByteRange `json:"byte_range"`
	SampleIndexes []int           `json:"sample_indexes"`
}

// Segment is one output clip mapping. During a transition overlap it contains
// one SourceRef for every participating source track.
type Segment struct {
	Index int `json:"index"`
	model.Interval
	Transition   bool        `json:"transition"`
	TransitionID string      `json:"transition_id,omitempty"`
	Sources      []SourceRef `json:"sources"`
}

// Build splits a normalized timeline at cuts and transition boundaries.
// If a requested cut lies in a damaged range, Build returns the complete
// segments before that point together with a CUT_POINT_IN_CORRUPT_RANGE error.
func Build(tl *model.Timeline, params Params) ([]Segment, error) {
	if tl == nil {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageCut, "nil timeline")
	}

	lo, hi := model.NS(0), tl.Duration
	boundaries := map[model.NS]struct{}{lo: {}, hi: {}}
	cutAt := map[model.NS]bool{}
	for _, c := range params.Cuts {
		if c.At < lo {
			return nil, trace.E(trace.CodeInvalidArgument, trace.StageCut, "cut %s at %d is before timeline", c.Label, c.At).WithTime(int64(c.At))
		}
		cutAt[c.At] = true
		if c.At < hi {
			boundaries[c.At] = struct{}{}
		}
	}
	for _, tr := range params.Transitions {
		if tr.Start > tr.End || tr.Start < lo {
			return nil, trace.E(trace.CodeInvalidArgument, trace.StageCut, "transition %q has invalid range [%d,%d)", tr.ID, tr.Start, tr.End)
		}
		if tr.Start < hi {
			boundaries[tr.Start] = struct{}{}
			cutAt[tr.Start] = true
		}
		if tr.End < hi {
			boundaries[tr.End] = struct{}{}
			cutAt[tr.End] = true
		}
	}

	points := make([]model.NS, 0, len(boundaries))
	for p := range boundaries {
		points = append(points, p)
	}
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })

	var segments []Segment
	for i := 0; i+1 < len(points); i++ {
		start, end := points[i], points[i+1]
		if start >= end {
			continue
		}
		if cutAt[start] {
			if derr := damageAt(tl, start); derr != nil {
				return segments, derr
			}
		}

		seg := Segment{Index: len(segments), Interval: model.Interval{Start: start, End: end}}
		for _, tr := range tl.Tracks {
			if ref, ok := mapTrackRange(tr, seg.Interval); ok {
				seg.Sources = append(seg.Sources, ref)
			}
		}
		for _, x := range params.Transitions {
			if _, ok := seg.Interval.Intersect(model.Interval{Start: x.Start, End: x.End}); ok {
				seg.Transition = true
				seg.TransitionID = x.ID
			}
		}
		segments = append(segments, seg)
	}

	// A cut exactly at the confirmed EOF is still the left edge of the next
	// interval, so a half-open damage range starting there contains it.
	orderedCuts := append([]Cut{}, params.Cuts...)
	sort.Slice(orderedCuts, func(i, j int) bool { return orderedCuts[i].At < orderedCuts[j].At })
	for _, c := range orderedCuts {
		if c.At >= hi {
			if derr := damageAt(tl, c.At); derr != nil {
				return segments, derr
			}
		}
	}
	return segments, nil
}

func damageAt(tl *model.Timeline, at model.NS) error {
	for _, d := range tl.Damages {
		if d.Range.Contains(at) {
			e := trace.E(
				trace.CodeCutPointCorrupt,
				trace.StageCut,
				"cut point %d lies in damaged track %d range [%d,%d)",
				at, d.TrackID, d.Range.Start, d.Range.End,
			).WithTrack(d.TrackID).WithTime(int64(at)).WithOffset(d.ByteOffset)
			if d.Error != nil {
				e.Underlying = d.Error
			}
			return e
		}
	}
	return nil
}

func mapTrackRange(tr *model.Track, wanted model.Interval) (SourceRef, bool) {
	if tr == nil || len(tr.Samples) == 0 {
		return SourceRef{}, false
	}
	timelineRange, ok := tr.Interval().Intersect(wanted)
	if !ok {
		return SourceRef{}, false
	}
	ref := SourceRef{
		TrackID:  tr.ID,
		Timeline: timelineRange,
		Local: model.Interval{
			Start: timelineRange.Start - tr.OffsetNS,
			End:   timelineRange.End - tr.OffsetNS,
		},
	}

	first := -1
	for pos, s := range tr.Samples {
		if s.End > timelineRange.Start && s.Start < timelineRange.End {
			if first == -1 {
				first = pos
				ref.TickStart = s.StartTick
				ref.ByteRange.Start = s.Range.Start
			}
			ref.TickEnd = s.StartTick + uint64(s.DurationTick)
			ref.ByteRange.End = s.Range.End
			ref.SampleIndexes = append(ref.SampleIndexes, s.Index)
		}
	}
	if first == -1 {
		return SourceRef{}, false
	}
	return ref, true
}
