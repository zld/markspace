// Package clock defines the single media time base shared by every stage.
//
// All audio and video timestamps are converted into ticks on one Timeline.
// Modules are forbidden from accumulating time themselves: they must call
// Convert to map their native units into ticks and Commit to reserve an
// interval. Committed intervals are immutable; any attempt to overwrite or
// overlap a committed interval is rejected so that retries can never change
// already published media time.
package clock

import "errors"

// DefaultTickRateHz is the canonical timeline rate (48 kHz).
const DefaultTickRateHz uint64 = 48000

// VideoClockRateHz is the conventional RTP video clock rate.
const VideoClockRateHz uint64 = 90000

// Kind identifies which media an interval belongs to.
type Kind uint8

const (
	KindAudio Kind = iota + 1
	KindVideo
)

// Interval is a half-open [Start, End) range of ticks on the Timeline.
type Interval struct {
	Start uint64
	End   uint64
}

// Len returns the interval length in ticks.
func (i Interval) Len() uint64 { return i.End - i.Start }

// ErrOverlap is returned when a commit would touch an already committed range.
var ErrOverlap = errors.New("clock: interval overlaps committed timeline")

// ErrEmpty is returned when a zero-length interval is committed.
var ErrEmpty = errors.New("clock: empty interval")

// Timeline is the unique owner of media time for one pipeline run.
type Timeline struct {
	tickRate  uint64
	cursors   [3]uint64 // index 0 unused; indexed by Kind
	committed [3][]Interval
}

// NewTimeline builds a timeline running at the given tick rate.
func NewTimeline(tickRate uint64) *Timeline {
	if tickRate == 0 {
		tickRate = DefaultTickRateHz
	}
	return &Timeline{tickRate: tickRate}
}

// TickRate returns the timeline tick rate in Hz.
func (t *Timeline) TickRate() uint64 { return t.tickRate }

// Convert maps `units` counted at unitRate (units per second) onto timeline
// ticks. The mapping uses integer floor division and is therefore exact and
// deterministic across retries; no floating-point state is ever accumulated.
func (t *Timeline) Convert(units, unitRate uint64) uint64 {
	if unitRate == 0 {
		return 0
	}
	return units * t.tickRate / unitRate
}

// Cursor returns the first uncommitted tick for the given kind.
func (t *Timeline) Cursor(k Kind) uint64 { return t.cursors[k] }

// Commit reserves [start, start+length) for the given kind. It rejects empty
// intervals and anything starting before the kind's cursor, which guarantees
// that committed time can never be rewritten by a retry or a late packet.
func (t *Timeline) Commit(k Kind, start, length uint64) (Interval, error) {
	if length == 0 {
		return Interval{}, ErrEmpty
	}
	if start < t.cursors[k] {
		return Interval{}, ErrOverlap
	}
	iv := Interval{Start: start, End: start + length}
	t.cursors[k] = iv.End
	t.committed[k] = append(t.committed[k], iv)
	return iv, nil
}

// Intervals returns a copy of the committed intervals for the given kind.
func (t *Timeline) Intervals(k Kind) []Interval {
	src := t.committed[k]
	out := make([]Interval, len(src))
	copy(out, src)
	return out
}

// TicksToMs converts a tick span into whole milliseconds (floor).
func (t *Timeline) TicksToMs(ticks uint64) uint64 {
	return ticks * 1000 / t.tickRate
}

// MsToTicks converts milliseconds into ticks at the timeline rate.
func (t *Timeline) MsToTicks(ms uint64) uint64 {
	return ms * t.tickRate / 1000
}
