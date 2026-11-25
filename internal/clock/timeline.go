package clock

import (
	"sort"

	"archivecheck/internal/diagnostic"
)

// Interval is one ordered item on the common nanosecond timeline.
type Interval struct {
	Start  Nanoseconds `json:"start_ns"`
	End    Nanoseconds `json:"end_ns"`
	Order  int         `json:"order"`
	Sample int         `json:"sample"`
	Offset int64       `json:"offset"`
	Label  string      `json:"label,omitempty"`
}

// Overlap identifies two input-order positions whose intervals overlap.
type Overlap struct {
	A int `json:"a"`
	B int `json:"b"`
}

// Timeline is a sortable, validated collection of intervals.
type Timeline struct {
	Intervals []Interval
}

// Add validates and appends one interval.
func (tl *Timeline) Add(in Interval) error {
	if in.Start < 0 {
		return diagnostic.New(diagnostic.CodeNegativeTime, diagnostic.PhaseClock, in.Sample, in.Offset,
			"interval start %d is negative", in.Start)
	}
	if in.End < in.Start {
		return diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, in.Sample, in.Offset,
			"interval end %d precedes start %d", in.End, in.Start)
	}
	tl.Intervals = append(tl.Intervals, in)
	return nil
}

// Sorted returns intervals sorted by start time. Intervals with equal starts
// retain their original input Order, giving stable output.
func (tl *Timeline) Sorted() []Interval {
	out := append([]Interval(nil), tl.Intervals...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].Order < out[j].Order
	})
	return out
}

// Normalize validates every interval and then applies stable ordering in place.
func (tl *Timeline) Normalize() error {
	for _, in := range tl.Intervals {
		if in.Start < 0 {
			return diagnostic.New(diagnostic.CodeNegativeTime, diagnostic.PhaseClock, in.Sample, in.Offset,
				"interval start %d is negative", in.Start)
		}
		if in.End < in.Start {
			return diagnostic.New(diagnostic.CodeInvalidTimecode, diagnostic.PhaseClock, in.Sample, in.Offset,
				"interval end %d precedes start %d", in.End, in.Start)
		}
	}
	tl.Intervals = tl.Sorted()
	return nil
}

// Overlaps returns overlapping pairs in stable sorted-timeline order. Adjacent
// intervals where one ends exactly when the next starts do not overlap.
func (tl *Timeline) Overlaps() []Overlap {
	sorted := tl.Sorted()
	var out []Overlap
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Start >= sorted[i].End {
				break
			}
			out = append(out, Overlap{A: sorted[i].Order, B: sorted[j].Order})
		}
	}
	return out
}

// Duration returns the span from zero to the latest interval end.
func (tl *Timeline) Duration() Nanoseconds {
	var end Nanoseconds
	for _, in := range tl.Intervals {
		if in.End > end {
			end = in.End
		}
	}
	return end
}
