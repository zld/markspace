package core

import "fmt"

// Span is a half-open byte interval [Start, End) inside one input asset.
// End may equal Start for a discontinuity that occupies no bytes; the
// interval is still reported so downstream retry logic has a stable anchor.
type Span struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// NewSpan validates a half-open interval.
func NewSpan(start, end int64) (Span, error) {
	if start < 0 || end < start {
		return Span{}, fmt.Errorf("core: invalid span [%d,%d)", start, end)
	}
	return Span{Start: start, End: end}, nil
}

// Len returns End-Start.
func (s Span) Len() int64 {
	if s.End < s.Start {
		return 0
	}
	return s.End - s.Start
}

// Empty reports whether the interval contains no bytes.
func (s Span) Empty() bool { return s.End <= s.Start }

// Contains reports whether off lies in [Start, End).
func (s Span) Contains(off int64) bool { return off >= s.Start && off < s.End }

// Within reports whether the span is inside [0,total].
func (s Span) Within(total int64) bool {
	return s.Start >= 0 && s.End >= s.Start && s.End <= total
}

// Union returns the smallest span covering both inputs.
func (s Span) Union(o Span) Span {
	if o.Start < s.Start {
		s.Start = o.Start
	}
	if o.End > s.End {
		s.End = o.End
	}
	return s
}
