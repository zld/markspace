// Package clock defines the shared media time base.
//
// All timeline arithmetic uses integer nanosecond ticks so that long-running
// pipelines never accumulate floating-point rounding error. Track-local media
// ticks (in the track's own timescale) are converted to/from the unified
// timeline with integer math, and every conversion documents its rounding
// rule explicitly.
package clock

import "math"

// TicksPerSecond is the resolution of the unified media timeline.
const TicksPerSecond int64 = 1_000_000_000

// Nanos is the scalar type of every point on the unified timeline.
type Nanos int64

// MaxNanos / MinNanos bound timeline values. Conversion functions reject
// values that would overflow this range instead of silently wrapping.
const (
	MaxNanos Nanos = math.MaxInt64
	MinNanos Nanos = math.MinInt64
)

// BasisVersion identifies the on-disk layout and semantics of the time base.
// Any change to timeline semantics, index records or digest rules must bump
// this constant so stale cases are rejected and rebuilt.
const BasisVersion uint32 = 1

// TimeBase describes a track-local clock: `Timescale` ticks per second,
// e.g. a 90 kHz video clock uses Timescale=90000.
type TimeBase struct {
	TrackID    uint32
	Timescale  uint32
	MediaStart int64 // earliest media-time value present in the track
}

// ToNanos converts track-local ticks to unified nanoseconds.
//
// Rounding rule: round half away from zero (Euclidean-style), i.e.
//
//	n * TicksPerSecond / Timescale with sign-preserving remainder correction.
//
// An overflow (scaled magnitude outside int64) is reported as ErrOverflow;
// a zero timescale is ErrInvalidArgument.
func (b TimeBase) ToNanos(ticks int64) (Nanos, error) {
	if b.Timescale == 0 {
		return 0, ErrInvalidArgument("clock: zero timescale")
	}
	num := ticks
	scale := int64(b.Timescale)
	// Detect |ticks * TicksPerSecond| > MaxInt64 before multiplying.
	if ticks != 0 && (ticks > 0 && ticks > math.MaxInt64/TicksPerSecond ||
		ticks < 0 && ticks < math.MinInt64/TicksPerSecond) {
		return 0, ErrOverflow("clock: tick->nanos overflow")
	}
	prod := num * TicksPerSecond
	q, r := prod/scale, prod%scale
	// Round half away from zero.
	if r != 0 {
		if (r > 0) == (scale > 0) {
			if abs64(r)*2 >= abs64(scale) {
				q++
			}
		} else {
			if abs64(r)*2 >= abs64(scale) {
				q--
			}
		}
	}
	return Nanos(q), nil
}

// ToTicks converts unified nanoseconds back to track-local ticks using the
// same round-half-away-from-zero rule, so ToTicks(ToNanos(t)) == t whenever
// the nanosecond value is an exact multiple of the track grid.
func (b TimeBase) ToTicks(n Nanos) (int64, error) {
	if b.Timescale == 0 {
		return 0, ErrInvalidArgument("clock: zero timescale")
	}
	scale := int64(b.Timescale)
	v := int64(n)
	if v != 0 && (v > 0 && v > math.MaxInt64/scale ||
		v < 0 && v < math.MinInt64/scale) {
		return 0, ErrOverflow("clock: nanos->tick overflow")
	}
	prod := v * scale
	q, r := prod/TicksPerSecond, prod%TicksPerSecond
	if r != 0 {
		if (r > 0) == (TicksPerSecond > 0) {
			if abs64(r)*2 >= TicksPerSecond {
				q++
			}
		} else {
			if abs64(r)*2 >= TicksPerSecond {
				q--
			}
		}
	}
	return q, nil
}

// DeltaNanos converts a *non-negative* track tick delta (e.g. stts duration)
// to nanoseconds. Negative deltas are invalid in sample tables.
func (b TimeBase) DeltaNanos(deltaTicks uint64) (Nanos, error) {
	if b.Timescale == 0 {
		return 0, ErrInvalidArgument("clock: zero timescale")
	}
	if deltaTicks > uint64(math.MaxInt64/TicksPerSecond) {
		return 0, ErrOverflow("clock: delta overflow")
	}
	v := int64(deltaTicks) * TicksPerSecond
	return Nanos(v / int64(b.Timescale)), nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
