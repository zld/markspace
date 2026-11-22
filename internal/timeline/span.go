package timeline

import (
	"errors"
	"sort"
)

// ErrBadSpan marks a span whose end precedes its start.
var ErrBadSpan = errors.New("timeline: span end before start")

// Span is a half-open tick interval [Start, End). Half-open intervals make
// adjacent fragments tile a timeline with no double counting and let an empty
// span be represented as Start == End.
type Span struct {
	Start int64
	End   int64
}

// NewSpan validates and returns a half-open span.
func NewSpan(start, end int64) (Span, error) {
	if end < start {
		return Span{}, ErrBadSpan
	}
	return Span{Start: start, End: end}, nil
}

// Len is the number of ticks covered. A half-open span of equal edges is empty.
func (s Span) Len() int64 { return s.End - s.Start }

// Empty reports whether the span covers no ticks.
func (s Span) Empty() bool { return s.End <= s.Start }

// Contains reports whether tick t is inside the half-open interval.
func (s Span) Contains(t int64) bool { return s.Start <= t && t < s.End }

// ContainsSpan reports whether other lies entirely inside s.
func (s Span) ContainsSpan(other Span) bool {
	return s.Start <= other.Start && other.End <= s.End
}

// Overlaps reports whether the two half-open spans share at least one tick.
// Touching spans (a.End == b.Start) do not overlap.
func (s Span) Overlaps(other Span) bool {
	return s.Start < other.End && other.Start < s.End
}

// Intersect returns the shared interval and false when they are disjoint.
func (s Span) Intersect(other Span) (Span, bool) {
	start := max64(s.Start, other.Start)
	end := min64(s.End, other.End)
	if end <= start {
		return Span{}, false
	}
	return Span{Start: start, End: end}, true
}

// Shift translates the span by delta ticks.
func (s Span) Shift(delta int64) Span { return Span{Start: s.Start + delta, End: s.End + delta} }

// MergeSpans sorts spans and unions overlapping or touching ones. The result
// is deterministic for equal inputs, which the repeat-run invariant relies on.
func MergeSpans(spans []Span) []Span {
	clean := make([]Span, 0, len(spans))
	for _, s := range spans {
		if !s.Empty() {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	sort.Slice(clean, func(i, j int) bool {
		if clean[i].Start != clean[j].Start {
			return clean[i].Start < clean[j].Start
		}
		return clean[i].End < clean[j].End
	})
	out := []Span{clean[0]}
	for _, s := range clean[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// Gaps returns the uncovered intervals inside [from, to), given covered spans
// which need not be sorted or disjoint.
func Gaps(from, to int64, covered []Span) []Span {
	if to <= from {
		return nil
	}
	merged := MergeSpans(covered)
	var gaps []Span
	cursor := from
	for _, s := range merged {
		if s.End <= cursor {
			continue
		}
		if s.Start >= to {
			break
		}
		if s.Start > cursor {
			gaps = append(gaps, Span{Start: cursor, End: min64(s.Start, to)})
		}
		cursor = max64(cursor, s.End)
		if cursor >= to {
			break
		}
	}
	if cursor < to {
		gaps = append(gaps, Span{Start: cursor, End: to})
	}
	return gaps
}

// BoundaryPolicy decides which segment owns a sample whose presentation time
// lands exactly on a segment edge. The two policies are exhaustive and
// mutually exclusive so every tick belongs to exactly one segment.
type BoundaryPolicy int

const (
	// EdgeOwnsLeft assigns a tick equal to a boundary to the earlier segment
	// (closed-left, closed-right at edges). This is the container default.
	EdgeOwnsLeft BoundaryPolicy = iota
	// EdgeOwnsRight assigns boundary ticks to the later segment (half-open
	// segments [start, end)), which tiles a timeline directly.
	EdgeOwnsRight
)

// SegmentIndex returns the zero-based segment that owns tick t. Ticks before
// the origin are clamped to segment 0; ticks at or after the last full segment
// belong to the trailing partial or empty segment.
func SegmentIndex(t, origin, duration int64, policy BoundaryPolicy) int64 {
	if duration <= 0 {
		return 0
	}
	rel := t - origin
	if rel <= 0 {
		return 0
	}
	idx := rel / duration
	rem := rel % duration
	switch policy {
	case EdgeOwnsRight:
		return idx
	default: // EdgeOwnsLeft
		if rem == 0 {
			return idx - 1
		}
		return idx
	}
}

// SegmentSpan returns the half-open tick interval of segment idx.
func SegmentSpan(idx, origin, duration int64) Span {
	if idx < 0 {
		idx = 0
	}
	start := origin + idx*duration
	return Span{Start: start, End: start + duration}
}

// SegmentCount is the number of segments needed to cover [start, end).
func SegmentCount(start, end, origin, duration int64) int64 {
	if end <= start || duration <= 0 {
		return 0
	}
	last := SegmentIndex(end-1, origin, duration, EdgeOwnsRight)
	return last + 1
}

// SampleBoundary is a sample's half-open tick footprint. A zero-length sample
// is promoted to one tick so it still belongs to exactly one segment.
type SampleBoundary struct {
	PTS int64
	DTS int64
	Dur int64
}

// Footprint returns the presentation interval of the sample.
func (b SampleBoundary) Footprint() Span {
	dur := b.Dur
	if dur <= 0 {
		dur = 1
	}
	return Span{Start: b.PTS, End: b.PTS + dur}
}

// Crosses reports whether the sample straddles a segment edge. Such samples
// are still assigned by their start tick, but the caller can report them as
// boundary-crossing evidence instead of silently splitting media.
func (b SampleBoundary) Crosses(origin, duration int64, policy BoundaryPolicy) bool {
	if duration <= 0 {
		return false
	}
	fp := b.Footprint()
	first := SegmentIndex(fp.Start, origin, duration, policy)
	last := SegmentIndex(fp.End-1, origin, duration, policy)
	return first != last
}

// DedupeTimestamps removes samples that repeat an already seen timestamp. The
// input order is preserved among survivors; when duplicates appear, the first
// occurrence wins so runs are stable regardless of map iteration. It returns
// the surviving indices and the dropped indices.
func DedupeTimestamps(pts []int64) (kept, dropped []int) {
	seen := make(map[int64]struct{}, len(pts))
	for i, t := range pts {
		if _, ok := seen[t]; ok {
			dropped = append(dropped, i)
			continue
		}
		seen[t] = struct{}{}
		kept = append(kept, i)
	}
	return kept, dropped
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
