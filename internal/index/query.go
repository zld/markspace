package index

import (
	"mediad/internal/clock"
)

// Segment is one exact closed-open interval [StartNs, EndNs) of the media,
// with the source byte extent needed to read it (starting at the keyframe
// that governs StartNs) and the events falling inside the interval.
type Segment struct {
	StartNs   int64
	EndNs     int64
	SrcOffset int64
	SrcLength int64
	Events    []TimedEvent
}

// Query returns the segment covering [startNs, endNs). Intervals outside
// the media extent are rejected with ErrBounds, never silently clamped.
func (ix *Index) Query(events []TimedEvent, startNs, endNs int64) (Segment, error) {
	if startNs < 0 || endNs <= startNs {
		return Segment{}, clock.New(clock.ErrBounds,
			"invalid interval [%d,%d)", startNs, endNs)
	}
	if startNs >= ix.DurationNs || endNs > ix.DurationNs {
		return Segment{}, clock.New(clock.ErrBounds,
			"interval [%d,%d) outside media duration %d", startNs, endNs, ix.DurationNs)
	}
	head := ix.keyAtOrBefore(startNs)
	if head == nil {
		head = ix.keyAtOrAfter(startNs)
	}
	if head == nil {
		return Segment{}, clock.New(clock.ErrBounds, "index has no keyframes")
	}
	srcEnd := ix.SrcLength
	if tail := ix.keyAtOrAfter(endNs); tail != nil && tail.Offset > head.Offset {
		srcEnd = tail.Offset
	}
	seg := Segment{
		StartNs:   startNs,
		EndNs:     endNs,
		SrcOffset: head.Offset,
		SrcLength: srcEnd - head.Offset,
	}
	for _, ev := range events {
		if ev.TimeNs >= startNs && ev.TimeNs < endNs {
			seg.Events = append(seg.Events, ev)
		}
	}
	return seg, nil
}
