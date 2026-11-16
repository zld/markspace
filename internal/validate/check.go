// Package validate applies user-supplied alignment tolerances to an align
// report and, separately, cross-checks a generated MPD against its plan.
package validate

import (
	"fmt"

	"dashgen/internal/align"
	"dashgen/internal/timebase"
)

// Violation kinds.
const (
	KindStartOffset   = "start_offset"
	KindDurationDelta = "duration_delta"
)

// Tolerance is the maximum permitted absolute deviation in milliseconds.
type Tolerance struct {
	StartMs    timebase.Rational
	DurationMs timebase.Rational
}

// Violation describes one segment whose deviation exceeds its tolerance.
type Violation struct {
	Kind    string
	RepID   string
	RefID   string
	Number  int64
	DeltaMs timebase.Rational
	LimitMs timebase.Rational
}

func (v Violation) Error() string {
	return fmt.Sprintf("validate: %s: rep %q segment %d vs ref %q: %s ms exceeds tolerance %s ms",
		v.Kind, v.RepID, v.Number, v.RefID,
		timebase.FormatMillis(v.DeltaMs), timebase.FormatMillis(v.LimitMs))
}

// Result bundles the applied tolerance with every violation found.
type Result struct {
	Tolerance  Tolerance
	Violations []Violation
}

// Failed reports whether any tolerance was exceeded.
func (r *Result) Failed() bool { return len(r.Violations) > 0 }

// CheckTolerances compares every rendition against the first representation,
// which acts as the reference. Only segment numbers present in every
// rendition are compared; missing segments are already reported by align.
// Comparisons stay in exact rational milliseconds.
func CheckTolerances(report *align.Report, tol Tolerance) (*Result, error) {
	if report == nil || len(report.Timelines) == 0 {
		return nil, fmt.Errorf("validate: empty alignment report")
	}
	if tol.StartMs.Num < 0 || tol.DurationMs.Num < 0 {
		return nil, fmt.Errorf("validate: tolerances must not be negative")
	}
	byRep := make(map[string]map[int64]align.SegmentBounds, len(report.Timelines))
	for _, tl := range report.Timelines {
		m := make(map[int64]align.SegmentBounds, len(tl.Bounds))
		for _, b := range tl.Bounds {
			m[b.Number] = b
		}
		byRep[tl.RepID] = m
	}
	ref := report.Timelines[0]
	res := &Result{Tolerance: tol}
	for i := 1; i < len(report.Timelines); i++ {
		cand := report.Timelines[i]
		for _, n := range report.SegmentNumbers {
			refSeg, ok1 := byRep[ref.RepID][n]
			candSeg, ok2 := byRep[cand.RepID][n]
			if !ok1 || !ok2 {
				continue // missing-segment defects come from align
			}
			startDelta, err := candSeg.StartMs.Sub(refSeg.StartMs)
			if err != nil {
				return nil, err
			}
			startDelta = startDelta.Abs()
			if startDelta.Cmp(tol.StartMs) > 0 {
				res.Violations = append(res.Violations, Violation{
					Kind: KindStartOffset, RepID: cand.RepID, RefID: ref.RepID,
					Number: n, DeltaMs: startDelta, LimitMs: tol.StartMs,
				})
			}
			durDelta, err := candSeg.Duration.Sub(refSeg.Duration)
			if err != nil {
				return nil, err
			}
			durDelta = durDelta.Abs()
			if durDelta.Cmp(tol.DurationMs) > 0 {
				res.Violations = append(res.Violations, Violation{
					Kind: KindDurationDelta, RepID: cand.RepID, RefID: ref.RepID,
					Number: n, DeltaMs: durDelta, LimitMs: tol.DurationMs,
				})
			}
		}
	}
	return res, nil
}
