// Package timeline normalizes Matroska ticks to a single nanosecond base and
// aligns multiple bitrate representations onto shared segment boundaries.
package timeline

import (
	"errors"
	"fmt"
	"sort"
)

// Slice is a normalized time interval on the shared base.
type Slice struct {
	Start int64 `json:"start_ns"`
	End   int64 `json:"end_ns"`
}

// Duration returns the slice length in nanoseconds.
func (s Slice) Duration() int64 { return s.End - s.Start }

// Representation is one bitrate rendition of the same media timeline.
type Representation struct {
	ID        string  `json:"id"`
	Bandwidth uint64  `json:"bandwidth"`
	Frames    []int64 `json:"-"` // keyframe starts in nanoseconds, sorted
	Duration  int64   `json:"duration_ns"`
}

// Alignment is the result of mapping representations onto shared cuts.
type Alignment struct {
	// Cuts are segment boundaries in nanoseconds, strictly increasing.
	Cuts []int64 `json:"cuts_ns"`
	// PerRepresentation lists, for each representation ID, the index of the
	// keyframe at or before each cut.
	PerRepresentation map[string][]int `json:"per_representation"`
	// Tolerance is the maximum allowed distance between a cut and the keyframe
	// chosen to start the segment.
	Tolerance int64 `json:"tolerance_ns"`
}

// AlignInput describes the alignment request.
type AlignInput struct {
	Representations []Representation
	TargetDuration  int64 // desired segment length in nanoseconds
	Tolerance       int64 // max distance from a cut to a keyframe
}

// AlignError reports a non-lossless alignment.
type AlignError struct {
	Representation string
	Cut            int64
	Detail         string
}

func (e *AlignError) Error() string {
	return fmt.Sprintf("timeline: cannot align representation %q at cut %d ns: %s", e.Representation, e.Cut, e.Detail)
}

// Align maps every representation to the same cut grid. A cut is accepted only
// if every representation has a keyframe within Tolerance of it; otherwise the
// whole alignment is rejected so callers can keep a resumable checkpoint.
func Align(in AlignInput) (*Alignment, error) {
	if in.TargetDuration <= 0 {
		return nil, errors.New("timeline: target duration must be positive")
	}
	if in.Tolerance < 0 {
		return nil, errors.New("timeline: tolerance must be non-negative")
	}
	if len(in.Representations) < 2 {
		return nil, errors.New("timeline: at least two representations are required")
	}
	reps := append([]Representation(nil), in.Representations...)
	sort.Slice(reps, func(i, j int) bool { return reps[i].ID < reps[j].ID })
	var maxDur int64
	for _, r := range reps {
		if len(r.Frames) == 0 {
			return nil, &AlignError{Representation: r.ID, Detail: "no keyframes"}
		}
		if r.Duration > maxDur {
			maxDur = r.Duration
		}
	}
	// Candidate cuts: every target boundary sliding to the nearest keyframe of
	// the lowest-bandwidth representation, then verified against all others.
	ref := reps[0]
	for i := 1; i < len(reps); i++ {
		if reps[i].Bandwidth < ref.Bandwidth {
			ref = reps[i]
		}
	}
	var cuts []int64
	for target := int64(0); target < maxDur; target += in.TargetDuration {
		idx := sort.Search(len(ref.Frames), func(i int) bool { return ref.Frames[i] >= target })
		if idx == len(ref.Frames) {
			idx = len(ref.Frames) - 1
		}
		// Prefer the earlier keyframe on ties so the grid cannot drift
		// forwards across repeated runs.
		if idx > 0 && abs(ref.Frames[idx]-target) >= abs(ref.Frames[idx-1]-target) {
			idx--
		}
		if ref.Frames[idx] >= maxDur {
			continue
		}
		cuts = append(cuts, ref.Frames[idx])
	}
	if len(cuts) == 0 {
		return nil, &AlignError{Representation: ref.ID, Detail: "no candidate cuts"}
	}
	// Deduplicate while preserving order.
	uniq := cuts[:1]
	for _, c := range cuts[1:] {
		if c != uniq[len(uniq)-1] {
			uniq = append(uniq, c)
		}
	}
	cuts = uniq
	per := make(map[string][]int, len(reps))
	for _, r := range reps {
		idxAt := make([]int, len(cuts))
		for i, cut := range cuts {
			j := sort.Search(len(r.Frames), func(k int) bool { return r.Frames[k] >= cut })
			switch {
			case j < len(r.Frames) && r.Frames[j] == cut:
				idxAt[i] = j
				continue
			case j == len(r.Frames):
				j = len(r.Frames) - 1
			case j > 0:
				if abs(r.Frames[j-1]-cut) <= abs(r.Frames[j]-cut) {
					j--
				}
			}
			delta := abs(r.Frames[j] - cut)
			if delta > in.Tolerance {
				return nil, &AlignError{Representation: r.ID, Cut: cut, Detail: fmt.Sprintf("nearest keyframe %d ns is %d ns away", r.Frames[j], delta)}
			}
			idxAt[i] = j
		}
		per[r.ID] = idxAt
	}
	return &Alignment{Cuts: cuts, PerRepresentation: per, Tolerance: in.Tolerance}, nil
}

// SegmentOf returns the segment index a timestamp belongs to given cuts.
func (a *Alignment) SegmentOf(nanos int64) int {
	return sort.Search(len(a.Cuts), func(i int) bool { return a.Cuts[i] > nanos }) - 1
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
