// Package align slices multiple bitrate representations at their common
// keyframe cut points on the normalized integer tick grid. Boundaries are
// never moved: when representations disagree on a cut point the conflict is
// reported with the offending tracks and original byte offsets.
package align

import (
	"fmt"
	"sort"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
	"gamepack/internal/source"
	"gamepack/internal/stream"
)

const subsystem = "align"

// Representation is one bitrate rendition of the same content.
type Representation struct {
	ID          string
	Codec       stream.Codec
	BitrateKbps int
	Scan        *stream.ScanResult
	Samples     []source.Sample // normalized transport, ordinal-aligned to units
}

// CutPoint is one shared segment boundary on the normalized timeline.
type CutPoint struct {
	Index       int                    `json:"index"`
	Normalized  clock.Tick             `json:"normalized_tick"`
	Ordinals    map[string]int         `json:"ordinals"`
	OrigOffsets map[string]int64       `json:"orig_offsets"`
}

// Segment is one time-aligned fragment across all representations.
type Segment struct {
	Index     int                  `json:"index"`
	StartTick clock.Tick           `json:"start_tick"`
	EndTick   clock.Tick           `json:"end_tick"`
	Cuts      map[string]TrackCut  `json:"cuts"`
}

// TrackCut records the per-representation unit range of a segment.
type TrackCut struct {
	StartOrdinal int   `json:"start_ordinal"`
	EndOrdinal   int   `json:"end_ordinal"` // exclusive
	StartOffset  int64 `json:"start_offset"`
	EndOffset    int64 `json:"end_offset"`
}

// Ladder is the aligned representation ladder.
type Ladder struct {
	Representations []*Representation
	CutPoints       []CutPoint
	Segments        []Segment
}

type tickBoundary struct {
	tick clock.Tick
	ev   stream.BoundaryEvent
}

// tickAt resolves the normalized tick of a boundary ordinal.
func (r *Representation) tickAt(ordinal int) (clock.Tick, error) {
	if ordinal < 0 || ordinal >= len(r.Samples) {
		return 0, perr.New(subsystem, perr.CodeBoundaryConflict,
			"representation %q missing normalized sample for ordinal %d", r.ID, ordinal)
	}
	return r.Samples[ordinal].NormalizedTick, nil
}

// boundariesByTick returns each representation's keyframe boundaries keyed
// by normalized tick, sorted in tick order.
func boundariesByTick(r *Representation) ([]tickBoundary, map[clock.Tick]stream.BoundaryEvent, error) {
	byTick := make(map[clock.Tick]stream.BoundaryEvent, len(r.Scan.Boundaries))
	for _, b := range r.Scan.Boundaries {
		tk, err := r.tickAt(b.Ordinal)
		if err != nil {
			return nil, nil, err
		}
		byTick[tk] = b
	}
	ordered := make([]tickBoundary, 0, len(byTick))
	for tk, ev := range byTick {
		ordered = append(ordered, tickBoundary{tick: tk, ev: ev})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].tick < ordered[j].tick })
	return ordered, byTick, nil
}

// Build aligns the representations at keyframe cut points whose normalized
// ticks match exactly. The first representation is the reference: every one
// of its keyframe ticks must also be a keyframe tick in all other tracks.
// Any mismatch fails with a conflict naming the tracks and original offsets;
// boundaries are never shifted to make the cuts fit.
func Build(reps []*Representation) (*Ladder, error) {
	if len(reps) == 0 {
		return nil, perr.New(subsystem, perr.CodeBoundaryConflict, "no representations")
	}
	if err := validateReps(reps); err != nil {
		return nil, err
	}

	orders := make([][]tickBoundary, len(reps))
	lookup := make([]map[clock.Tick]stream.BoundaryEvent, len(reps))
	for i, r := range reps {
		ordered, byTick, err := boundariesByTick(r)
		if err != nil {
			return nil, err
		}
		orders[i], lookup[i] = ordered, byTick
	}

	// Reference track defines the required cut ticks.
	ref := reps[0]
	cuts := make([]CutPoint, 0, len(orders[0]))
	for _, tb := range orders[0] {
		cp := CutPoint{
			Index:       len(cuts),
			Normalized:  tb.tick,
			Ordinals:    map[string]int{},
			OrigOffsets: map[string]int64{},
		}
		for j, r := range reps {
			ev, ok := lookup[j][tb.tick]
			if !ok {
				return nil, conflictAtTick(ref, r, tb.tick, orders[j])
			}
			cp.Ordinals[r.ID] = ev.Ordinal
			cp.OrigOffsets[r.ID] = ev.Offset
		}
		cuts = append(cuts, cp)
	}
	if len(cuts) == 0 {
		return nil, perr.New(subsystem, perr.CodeBoundaryConflict,
			"reference representation %q has no keyframe cuts", ref.ID)
	}

	segs, err := buildSegments(reps, cuts)
	if err != nil {
		return nil, err
	}
	return &Ladder{Representations: reps, CutPoints: cuts, Segments: segs}, nil
}

func validateReps(reps []*Representation) error {
	ids := map[string]bool{}
	for _, r := range reps {
		if r == nil || r.Scan == nil || len(r.Samples) == 0 {
			return perr.New(subsystem, perr.CodeBoundaryConflict, "empty representation")
		}
		if ids[r.ID] {
			return perr.New(subsystem, perr.CodeBoundaryConflict,
				"duplicate representation id %q", r.ID)
		}
		ids[r.ID] = true
		if len(r.Samples) != len(r.Scan.Units) {
			return perr.New(subsystem, perr.CodeBoundaryConflict,
				"representation %q has %d samples but %d units",
				r.ID, len(r.Samples), len(r.Scan.Units))
		}
	}
	return nil
}

// buildSegments turns consecutive cut points into per-track unit ranges. The
// final segment runs to the end of each representation.
func buildSegments(reps []*Representation, cuts []CutPoint) ([]Segment, error) {
	segs := make([]Segment, 0, len(cuts))
	for i, cp := range cuts {
		var endTick clock.Tick
		if i+1 < len(cuts) {
			endTick = cuts[i+1].Normalized
		} else {
			endTick = reps[0].Samples[len(reps[0].Samples)-1].NormalizedTick
		}
		seg := Segment{
			Index: i, StartTick: cp.Normalized, EndTick: endTick,
			Cuts: map[string]TrackCut{},
		}
		for _, r := range reps {
			startOrd := cp.Ordinals[r.ID]
			endOrd := len(r.Scan.Units)
			if i+1 < len(cuts) {
				endOrd = cuts[i+1].Ordinals[r.ID]
			}
			if startOrd >= endOrd {
				return nil, perr.At(subsystem, perr.CodeBoundaryConflict,
					r.Scan.Units[startOrd].Start,
					"representation %q segment %d has empty unit range", r.ID, i)
			}
			seg.Cuts[r.ID] = TrackCut{
				StartOrdinal: startOrd, EndOrdinal: endOrd,
				StartOffset: r.Scan.Units[startOrd].Start,
				EndOffset:   r.Scan.Units[endOrd-1].End,
			}
		}
		segs = append(segs, seg)
	}
	return segs, nil
}

// conflictAtTick reports the reference cut and the offending track's nearest
// keyframe boundaries with their original offsets. Nothing is moved.
func conflictAtTick(ref, offender *Representation, tick clock.Tick,
	offenderOrder []tickBoundary) error {
	var nearest string
	if len(offenderOrder) > 0 {
		pick := offenderOrder[0]
		for _, tb := range offenderOrder {
			pick = tb
			if tb.tick >= tick {
				break
			}
		}
		nearest = fmt.Sprintf("; nearest %s cut tick=%d offset=%d",
			offender.ID, pick.tick, pick.ev.Offset)
	}
	return perr.New(subsystem, perr.CodeBoundaryConflict,
		"representation %q requires cut at tick %d but track %q has no keyframe there%s",
		ref.ID, tick, offender.ID, nearest)
}
