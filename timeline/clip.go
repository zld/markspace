package timeline

import (
	"sort"

	"mediapack/merr"
)

// Clip selects a half-open source range [SourceIn, SourceOut) and places
// it on the shared timeline at TimelineStart.
type Clip struct {
	TrackID       string
	SourceIn      int64 // nanoseconds, inclusive
	SourceOut     int64 // nanoseconds, exclusive
	TimelineStart int64 // nanoseconds on the shared timeline
}

// Duration returns the clip length in nanoseconds.
func (c Clip) Duration() int64 { return c.SourceOut - c.SourceIn }

// Contains reports whether source time t (ns) is inside the half-open range.
func (c Clip) Contains(srcNanos int64) bool {
	return srcNanos >= c.SourceIn && srcNanos < c.SourceOut
}

// Validate rejects negative times, zero-length and inverted ranges.
func (c Clip) Validate() error {
	if c.SourceIn < 0 || c.SourceOut < 0 || c.TimelineStart < 0 {
		return merr.Corruptf("clip", c.TrackID, "negative time in clip [%d,%d) @%d", c.SourceIn, c.SourceOut, c.TimelineStart)
	}
	if c.SourceOut <= c.SourceIn {
		return merr.Corruptf("clip", c.TrackID, "zero or negative length clip [%d,%d)", c.SourceIn, c.SourceOut)
	}
	return nil
}

// Transition blends FromTrack into ToTrack over [Start, Start+Duration).
type Transition struct {
	ID        string
	FromTrack string
	ToTrack   string
	Start     int64
	Duration  int64
	Kind      string // "crossfade"
}

// Validate rejects zero-length, negative and out-of-range references.
func (t Transition) Validate(g *Graph) error {
	if t.Duration <= 0 {
		return merr.Corruptf("transition", t.ID, "zero or negative duration %d", t.Duration)
	}
	if t.Start < 0 {
		return merr.Corruptf("transition", t.ID, "negative start %d", t.Start)
	}
	if t.Kind != "crossfade" {
		return merr.Corruptf("transition", t.ID, "unsupported kind %q", t.Kind)
	}
	if _, ok := g.Get(t.FromTrack); !ok {
		return merr.Corruptf("transition", t.ID, "unknown from-track %q", t.FromTrack)
	}
	if _, ok := g.Get(t.ToTrack); !ok {
		return merr.Corruptf("transition", t.ID, "unknown to-track %q", t.ToTrack)
	}
	return nil
}

// ValidateTransitions validates each transition and checks that any
// overlaps are well-ordered on the timeline (sorted by start time).
func ValidateTransitions(ts []Transition, g *Graph) error {
	for _, t := range ts {
		if err := t.Validate(g); err != nil {
			return err
		}
	}
	sorted := SortedTransitions(ts)
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if cur.Start < prev.Start {
			return merr.Corruptf("transition", cur.ID, "transitions out of timeline order")
		}
	}
	return nil
}

// SortedTransitions returns transitions ordered by start time (stable),
// which is the order overlapping blends are applied in.
func SortedTransitions(ts []Transition) []Transition {
	out := make([]Transition, len(ts))
	copy(out, ts)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// BlendWeight returns the ToTrack weight in [0,1] at timeline time t.
func BlendWeight(tNanos int64, tr Transition) float64 {
	if tNanos < tr.Start {
		return 0
	}
	if tNanos >= tr.Start+tr.Duration {
		return 1
	}
	return float64(tNanos-tr.Start) / float64(tr.Duration)
}

// ActiveTransitions returns transitions covering time t, in timeline order.
func ActiveTransitions(ts []Transition, tNanos int64) []Transition {
	var out []Transition
	for _, tr := range SortedTransitions(ts) {
		if tNanos >= tr.Start && tNanos < tr.Start+tr.Duration {
			out = append(out, tr)
		}
	}
	return out
}
