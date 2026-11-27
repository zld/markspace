// Package media defines the shared integer time base and the structured error
// model used by every processing stage of the regression kernel.
package media

import "math"

// Ticks is an absolute or relative instant on the nanosecond timeline. All
// subsystems use this single integer scale so segment boundaries, silence
// windows and fingerprints can be compared without floating point drift.
type Ticks int64

// Nanosecond is one tick. The named constants keep call sites readable.
const (
	Nanosecond  Ticks = 1
	Microsecond       = 1000 * Nanosecond
	Millisecond       = 1000 * Microsecond
	Second            = 1000 * Millisecond
)

// FrameRate is a rational rate. Den must be positive and Num non-negative.
type FrameRate struct {
	Num int64
	Den int64
}

// Validate reports whether the rate is usable as a time base.
func (r FrameRate) Validate() error {
	if r.Num <= 0 || r.Den <= 0 {
		return Errorf(ClassInvalidTimebase, StageTimeline, "", -1, "rate %d/%d", r.Num, r.Den)
	}
	return nil
}

// Ticks converts a frame count at this rate to nanoseconds, rounding down.
func (r FrameRate) Ticks(frames int64) (Ticks, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	return FractionTicks(frames*r.Den, r.Num)
}

// FractionTicks converts num/den seconds to nanoseconds. The result is rounded
// down (toward zero for negative values). Both overflow paths are checked so a
// hostile file cannot wrap the timeline into the past.
func FractionTicks(num, den int64) (Ticks, error) {
	if den <= 0 {
		return 0, Errorf(ClassInvalidTimebase, StageTimeline, "", -1, "denominator %d", den)
	}
	if num == 0 {
		return 0, nil
	}
	neg := num < 0
	if neg {
		num = -num
	}
	// Split the division so num*Second cannot overflow before the divide.
	q := num / den
	rem := num % den
	if q > math.MaxInt64/int64(Second) {
		return 0, Errorf(ClassOutOfRange, StageTimeline, "", -1, "duration overflows int64 ticks")
	}
	out := q*int64(Second) + rem*int64(Second)/den
	if neg {
		out = -out
	}
	return Ticks(out), nil
}

// SampleTicks converts a cumulative sample count at timescale units per second
// into ticks. timescale is the container media timescale (for example 48000).
func SampleTicks(samples int64, timescale uint32) (Ticks, error) {
	if timescale == 0 {
		return 0, Errorf(ClassInvalidTimebase, StageTimeline, "", -1, "zero media timescale")
	}
	return FractionTicks(samples, int64(timescale))
}

// SamplesAtTicks converts a nanosecond instant to the smallest cumulative
// sample count at or after t. It is used to align cuts to container samples.
func SamplesAtTicks(t Ticks, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, Errorf(ClassInvalidTimebase, StageTimeline, "", -1, "zero media timescale")
	}
	if t < 0 {
		return 0, Errorf(ClassTimeRegression, StageTimeline, "", -1, "negative instant %d", t)
	}
	// ceil(t * timescale / Second) without overflowing.
	q := int64(t) / int64(Second)
	rem := int64(t) % int64(Second)
	if q > math.MaxInt64/int64(timescale) {
		return 0, Errorf(ClassOutOfRange, StageTimeline, "", -1, "instant %d overflows", t)
	}
	whole := q * int64(timescale)
	part := rem * int64(timescale)
	samples := whole + part/int64(Second)
	if part%int64(Second) != 0 {
		samples++
	}
	return samples, nil
}

// Window is a half-open span [Start, End) on the shared timeline.
type Window struct {
	Start Ticks `json:"start_ns"`
	End   Ticks `json:"end_ns"`
}

// Validate rejects inverted or negative windows.
func (w Window) Validate() error {
	if w.Start < 0 {
		return Errorf(ClassTimeRegression, StageTimeline, "", -1, "negative window start %d", w.Start)
	}
	if w.End < w.Start {
		return Errorf(ClassTimeRegression, StageTimeline, "", -1, "window end %d before start %d", w.End, w.Start)
	}
	return nil
}

// Duration returns End-Start, or zero for an inverted window.
func (w Window) Duration() Ticks {
	if w.End < w.Start {
		return 0
	}
	return w.End - w.Start
}

// Contains reports whether t is inside the half-open window.
func (w Window) Contains(t Ticks) bool { return t >= w.Start && t < w.End }

// Overlaps reports whether two half-open windows share any instant.
func (w Window) Overlaps(o Window) bool { return w.Start < o.End && o.Start < w.End }

// Intersect returns the overlap of two windows and whether it is non-empty.
func (w Window) Intersect(o Window) (Window, bool) {
	start := w.Start
	if o.Start > start {
		start = o.Start
	}
	end := w.End
	if o.End < end {
		end = o.End
	}
	if end <= start {
		return Window{}, false
	}
	return Window{Start: start, End: end}, true
}
