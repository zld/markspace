// Package repair converts each track's stored timestamps onto a monotonic
// 64-bit presentation timeline. It expands 32-bit RTP-style wrap points,
// clamps duplicate and backward values, and records both the original value
// and the applied correction for every sample it touches.
package repair

import (
	"archivist/internal/clock"
	"archivist/internal/fault"
	"archivist/internal/scan"
)

// Version identifies the repair rules recorded in provenance.
const Version = "repair/v1"

// Correction kinds.
const (
	KindUnchanged = "unchanged"
	KindWrap      = "wrap"
	KindDuplicate = "duplicate"
	KindBackward  = "backward"
)

// Policy controls how aggressive the repairer may be. Backward jumps beyond
// MaxBackwardTicks are treated as ambiguous rather than silently rewritten.
type Policy struct {
	MaxBackwardTicks int64
	Step             int64
}

// DefaultPolicy derives a policy from the track timescale. A backward jump of
// more than one second is not repaired: it cannot be distinguished from a
// deliberate timeline reset, so processing stops instead.
func DefaultPolicy(timescale uint32) Policy {
	return Policy{MaxBackwardTicks: int64(timescale), Step: 0}
}

// Entry preserves the source and the result of one timestamp decision.
type Entry struct {
	Sample    int    `json:"sample"`
	Raw       uint32 `json:"raw"`
	Original  int64  `json:"original"` // wrap-expanded stored PTS
	Repaired  int64  `json:"repaired"` // monotonic presentation time
	Delta     int64  `json:"delta"`    // repaired - original
	Kind      string `json:"kind"`
	Keyframe  bool   `json:"keyframe"`
	Size      uint32 `json:"size"`
	Offset    int64  `json:"offset"`
	MovieTime int64  `json:"movie_time"` // repaired PTS in the movie timescale
}

// TrackResult is the repaired timeline for one track.
type TrackResult struct {
	TrackID        uint32  `json:"track_id"`
	Timescale      uint32  `json:"timescale"`
	MovieTimescale uint32  `json:"movie_timescale"`
	Handler        string  `json:"handler"`
	Width          uint32  `json:"width"`
	Height         uint32  `json:"height"`
	Step           int64   `json:"step"`
	Entries        []Entry `json:"entries"`
}

// RepairedCount reports how many entries required a non-trivial repair.
func (t *TrackResult) RepairedCount() int {
	n := 0
	for _, e := range t.Entries {
		if e.Kind != KindUnchanged {
			n++
		}
	}
	return n
}

// Result aggregates all tracks of one input.
type Result struct {
	RulesVersion   string         `json:"rules_version"`
	MovieTimescale uint32         `json:"movie_timescale"`
	Tracks         []*TrackResult `json:"tracks"`
}

// TotalRepaired returns the number of repaired entries across tracks.
func (r *Result) TotalRepaired() int {
	n := 0
	for _, t := range r.Tracks {
		n += t.RepairedCount()
	}
	return n
}

// Repair applies the policy to every track in c.
func Repair(c *scan.Container, policy Policy) (*Result, error) {
	if c.MovieTimescale == 0 {
		return nil, fault.New(fault.KindCorrupt, fault.StageRepair,
			"movie timescale is undefined").In(c.File).At(0)
	}
	res := &Result{RulesVersion: Version, MovieTimescale: c.MovieTimescale}
	for _, tr := range c.Tracks {
		out, err := repairTrack(c, tr, policy)
		if err != nil {
			return nil, err
		}
		res.Tracks = append(res.Tracks, out)
	}
	return res, nil
}

func repairTrack(c *scan.Container, tr *scan.Track, policy Policy) (*TrackResult, error) {
	if tr.Timescale == 0 {
		return nil, fault.Timestamp(fault.StageRepair, c.File, 0, tr.ID, tr.Timescale,
			"track timescale is zero")
	}
	if len(tr.Samples) == 0 {
		return nil, fault.Timestamp(fault.StageRepair, c.File, 0, tr.ID, tr.Timescale,
			"track has no samples")
	}
	step := policy.Step
	if step <= 0 {
		step = nominalStep(tr)
	}
	maxBack := policy.MaxBackwardTicks
	if maxBack <= 0 {
		maxBack = int64(tr.Timescale)
	}

	out := &TrackResult{
		TrackID:        tr.ID,
		Timescale:      tr.Timescale,
		MovieTimescale: c.MovieTimescale,
		Handler:        tr.Handler,
		Width:          tr.Width,
		Height:         tr.Height,
		Step:           step,
	}

	unwrapper := &clock.Unwrapper{}
	var prevExpanded, prevRepaired int64
	for i, s := range tr.Samples {
		raw := uint32(s.PTS)
		expanded, wrapped := unwrapper.Push(raw)
		repaired := expanded
		kind := KindUnchanged
		switch {
		case wrapped:
			// The expanded value continues the timeline across 2^32; the raw
			// 32-bit pattern alone would have jumped backwards.
			kind = KindWrap
		case i > 0 && expanded < prevExpanded:
			if prevExpanded-expanded > maxBack {
				return nil, fault.Timestamp(fault.StageRepair, c.File, s.Offset, tr.ID, tr.Timescale,
					"sample %d jumps backwards by %d ticks (limit %d); repair value cannot be determined",
					s.Index, prevExpanded-expanded, maxBack)
			}
			repaired = prevRepaired + step
			kind = KindBackward
		case i > 0 && expanded == prevExpanded:
			repaired = prevRepaired + step
			kind = KindDuplicate
		}
		if i > 0 && repaired <= prevRepaired && kind == KindUnchanged {
			// A sub-step advance still has to move forward for strict
			// monotonicity, which is what random-access seeking relies on.
			repaired = prevRepaired + step
			kind = KindBackward
		}
		out.Entries = append(out.Entries, Entry{
			Sample:   s.Index,
			Raw:      raw,
			Original: int64(raw),
			Repaired: repaired,
			// Delta always converts the stored value into the repaired one,
			// including the 2^32 shift for a wrap.
			Delta:     repaired - int64(raw),
			Kind:      kind,
			Keyframe:  s.Keyframe,
			Size:      s.Size,
			Offset:    s.Offset,
			MovieTime: clock.ScaleTicks(repaired, tr.Timescale, c.MovieTimescale),
		})
		prevExpanded = expanded
		prevRepaired = repaired
	}
	return out, nil
}

// nominalStep derives the expected inter-sample step from decode deltas,
// falling back to one tick.
func nominalStep(tr *scan.Track) int64 {
	if len(tr.Samples) > 1 {
		if d := tr.Samples[1].DTS - tr.Samples[0].DTS; d > 0 {
			return d
		}
	}
	return 1
}

// VerifyMonotonic checks a repaired result for strict monotonicity and round
// trip consistency. It is used by tests and the pipeline self-check.
func VerifyMonotonic(r *Result) error {
	for _, tr := range r.Tracks {
		var prev int64
		for i, e := range tr.Entries {
			if i > 0 && e.Repaired <= prev {
				return fault.New(fault.KindInvariant, fault.StageRepair,
					"track %d sample %d is not strictly increasing (%d <= %d)",
					tr.TrackID, e.Sample, e.Repaired, prev)
			}
			if e.Original+e.Delta != e.Repaired {
				return fault.New(fault.KindInvariant, fault.StageRepair,
					"track %d sample %d correction does not add up", tr.TrackID, e.Sample)
			}
			prev = e.Repaired
		}
	}
	return nil
}
