// Package segment plans keyframe-aligned cut points from the timeline and
// renders deterministic, hashable manifests shared by every rendition.
package segment

import (
	"sort"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
	"mediaverify/internal/timeline"
)

// Segment is one planned slice of the asset.
type Segment struct {
	Index     int
	Start     clock.Ticks
	End       clock.Ticks
	Untrusted bool
	Reasons   []string
	// AlignmentFrames is the cut's distance from the ideal grid in frames.
	AlignmentFrames int
}

// Plan is the full cut plan for one frame standard.
type Plan struct {
	FrameRate         clock.FrameRate
	TargetDuration    clock.Ticks
	Segments          []Segment
	AlignmentMaxFrame int
}

// Planner places cuts on the fixed-duration grid, snapping each cut to the
// nearest keyframe. The snap is rejected if it would exceed one frame.
type Planner struct {
	fr     clock.FrameRate
	target clock.Ticks
}

// NewPlanner validates the frame rate and target duration.
func NewPlanner(fr clock.FrameRate, target clock.Ticks) (*Planner, error) {
	if target <= 0 {
		return nil, errs.New(errs.KindInvalidInput, "segment", "target duration must be positive")
	}
	if fr.Nominal <= 0 || fr.Den <= 0 {
		return nil, errs.New(errs.KindInvalidInput, "segment", "invalid frame rate %s", fr.Name)
	}
	return &Planner{fr: fr, target: target}, nil
}

// Plan derives cuts from keyframe events. Untrusted intervals mark any
// segment they touch; slicing resumes at the first valid keyframe at or
// after the interval, so no content is ever fabricated across a loss.
func (p *Planner) Plan(events []timeline.Event, intervals []timeline.Interval, duration clock.Ticks) (Plan, error) {
	if duration <= 0 {
		return Plan{}, errs.New(errs.KindInvalidInput, "segment", "asset duration must be positive")
	}
	var keyframes []clock.Ticks
	for _, ev := range events {
		if ev.Keyframe {
			keyframes = append(keyframes, ev.Time)
		}
	}
	sort.Slice(keyframes, func(i, j int) bool { return keyframes[i] < keyframes[j] })

	frame := p.fr.FrameDurationNS()
	var cuts []clock.Ticks
	var alignFrames []int
	maxAlign := 0
	for grid := clock.Ticks(0); grid < duration; grid += p.target {
		if grid == 0 {
			cuts = append(cuts, 0)
			alignFrames = append(alignFrames, 0)
			continue
		}
		kf, dist, ok := nearestKeyframe(keyframes, grid)
		if !ok {
			return Plan{}, errs.New(errs.KindInvalidInput, "segment",
				"no keyframe available for cut near %d ns", grid.Ns())
		}
		framesOff := int((clock.Abs(dist) + frame/2) / frame)
		if framesOff > 1 {
			return Plan{}, errs.New(errs.KindInvalidInput, "segment",
				"cut at %d would be %d frames from keyframe %d, limit is 1",
				grid.Ns(), framesOff, kf.Ns())
		}
		cuts = append(cuts, kf)
		alignFrames = append(alignFrames, framesOff)
		if framesOff > maxAlign {
			maxAlign = framesOff
		}
	}
	cuts = append(cuts, duration)

	segs := make([]Segment, 0, len(cuts)-1)
	for i := 0; i < len(cuts)-1; i++ {
		seg := Segment{Index: i, Start: cuts[i], End: cuts[i+1], AlignmentFrames: alignFrames[i]}
		for _, in := range intervals {
			if in.Start < seg.End && in.End > seg.Start {
				seg.Untrusted = true
				seg.Reasons = append(seg.Reasons, in.Reason)
			}
		}
		segs = append(segs, seg)
	}
	return Plan{
		FrameRate:         p.fr,
		TargetDuration:    p.target,
		Segments:          segs,
		AlignmentMaxFrame: maxAlign,
	}, nil
}

// nearestKeyframe returns the keyframe closest to t, its signed distance and
// false when no keyframe exists.
func nearestKeyframe(keyframes []clock.Ticks, t clock.Ticks) (clock.Ticks, clock.Ticks, bool) {
	if len(keyframes) == 0 {
		return 0, 0, false
	}
	i := sort.Search(len(keyframes), func(i int) bool { return keyframes[i] >= t })
	best := keyframes[0]
	switch {
	case i == 0:
		best = keyframes[0]
	case i == len(keyframes):
		best = keyframes[len(keyframes)-1]
	default:
		after := keyframes[i]
		before := keyframes[i-1]
		if clock.Abs(after-t) < clock.Abs(t-before) {
			best = after
		} else {
			best = before
		}
	}
	return best, best - t, true
}

// RecoveredCount counts trusted segments following an untrusted run: each
// such segment represents a successful reslice at a valid keyframe.
func (p Plan) RecoveredCount() int {
	recovered := 0
	prevUntrusted := false
	for _, s := range p.Segments {
		if prevUntrusted && !s.Untrusted {
			recovered++
		}
		prevUntrusted = s.Untrusted
	}
	return recovered
}

// DegradedIntervals returns the untrusted segments as reportable spans.
func (p Plan) DegradedIntervals() []Segment {
	var out []Segment
	for _, s := range p.Segments {
		if s.Untrusted {
			out = append(out, s)
		}
	}
	return out
}
