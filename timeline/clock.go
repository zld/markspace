package timeline

import (
	"math"
)

// Clock converts between integer sample positions and nanosecond wall time.
// It is immutable after construction; every conversion uses exact integer
// division with an explicit rounding policy, so repeated conversions cannot
// accumulate floating point drift.
type Clock struct {
	rate uint32 // samples per second, e.g. 48000
}

// NewClock validates and builds a Clock for the given sample rate.
func NewClock(rate uint32) (Clock, error) {
	if rate == 0 {
		return Clock{}, NewError(ErrCodeIllegalBlockLength, StageTimeline, "", "sample rate must be non-zero")
	}
	return Clock{rate: rate}, nil
}

// Rate returns the samples-per-second rate.
func (c Clock) Rate() uint32 { return c.rate }

// SamplesToNanos converts a sample count to whole nanoseconds, rounding to
// nearest with half away from zero.
func (c Clock) SamplesToNanos(samples int64) int64 {
	if samples == 0 {
		return 0
	}
	num := samples * 1_000_000_000
	den := int64(c.rate)
	if num >= 0 {
		return (num + den/2) / den
	}

	return -((-num + den/2) / den)
}

// NanosToSamples converts a nanosecond timestamp to the sample grid, rounding
// to nearest. It is the exact inverse policy of SamplesToNanos.
func (c Clock) NanosToSamples(nanos int64) int64 {
	if nanos == 0 {
		return 0
	}
	num := nanos * int64(c.rate)
	den := int64(1_000_000_000)
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// SamplesToMillis rounds a sample count to whole milliseconds.
func (c Clock) SamplesToMillis(samples int64) int64 {
	return c.SamplesToNanos(samples) / 1_000_000
}

// NanosToMillis truncates nanoseconds to milliseconds without rounding.
func NanosToMillis(nanos int64) int64 {
	if nanos >= 0 {
		return nanos / 1_000_000
	}
	return -((-nanos) / 1_000_000)
}

// FrameDurationNanos is the exact duration of one sample as a rational number
// pair (numerator, denominator) for callers that need fraction-aware math.
func (c Clock) FrameDurationNanos() (num, den int64) {
	return 1_000_000_000, int64(c.rate)
}

// Interval is a half-open [Start, End) range on the integer sample grid.
// Length is always End-Start and a zero-length interval is explicitly allowed
// for flush markers, but negative lengths are rejected.
type Interval struct {
	Start int64
	End   int64
}

// Length returns the sample count covered by the interval.
func (iv Interval) Length() int64 { return iv.End - iv.Start }

// Contains reports whether iv fully covers other.
func (iv Interval) Contains(other Interval) bool {
	return other.Start >= iv.Start && other.End <= iv.End
}

// Overlaps reports whether the two half-open intervals share any sample.
func (iv Interval) Overlaps(other Interval) bool {
	return iv.Start < other.End && other.Start < iv.End
}

// Adjacent reports whether other begins exactly where iv ends (or vice versa).
func (iv Interval) Adjacent(other Interval) bool {
	return iv.End == other.Start || other.End == iv.Start
}

// Gap returns the uncovered interval between two non-overlapping intervals.
// The boolean is false when they overlap or touch.
func (iv Interval) Gap(other Interval) (Interval, bool) {
	if iv.Overlaps(other) || iv.Adjacent(other) {
		return Interval{}, false
	}
	if iv.End < other.Start {
		return Interval{Start: iv.End, End: other.Start}, true
	}
	return Interval{Start: other.End, End: iv.Start}, true
}

// Validate rejects negative-length intervals.
func (iv Interval) Validate(trackID string) error {
	if iv.End < iv.Start {
		return NewError(ErrCodeOutOfBounds, StageTimeline, trackID,
			"interval end %d precedes start %d", iv.End, iv.Start)
	}
	return nil
}

// Nanos renders both endpoints in nanoseconds for manifest output.
func (iv Interval) Nanos(c Clock) (startNanos, endNanos int64) {
	return c.SamplesToNanos(iv.Start), c.SamplesToNanos(iv.End)
}

// AlignMillis returns the absolute difference, in milliseconds, between the
// interval duration expressed on the sample grid and a reference nanosecond
// duration.
func (iv Interval) AlignMillis(c Clock, referenceNanos int64) int64 {
	delta := c.SamplesToNanos(iv.Length()) - referenceNanos
	if delta < 0 {
		delta = -delta
	}
	return delta / 1_000_000
}

// SaturatingMul guards byte-size math from overflow on hostile inputs.
func SaturatingMul(a, b int64) int64 {
	if a != 0 && b > math.MaxInt64/a {
		return math.MaxInt64
	}
	return a * b
}
