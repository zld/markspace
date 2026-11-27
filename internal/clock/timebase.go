// Package clock defines the single nanosecond time base shared by every
// stage of the continuity pipeline. All durations and timestamps crossing
// package boundaries are int64 nanoseconds. Conversions from other time
// units are checked for invalid ticks and integer overflow.
package clock

import (
	"errors"
	"math"
)

// NanosecondsPerSecond is the resolution of the canonical time base.
const NanosecondsPerSecond int64 = 1_000_000_000

// MaxNanos is the largest representable canonical timestamp.
const MaxNanos int64 = math.MaxInt64

// MinNanos is the smallest representable canonical timestamp.
const MinNanos int64 = math.MinInt64

var (
	// ErrInvalidTick means a time unit with zero or negative ticks per second.
	ErrInvalidTick = errors.New("clock: invalid ticks per second")
	// ErrOverflow means an addition or conversion overflows int64.
	ErrOverflow = errors.New("clock: time arithmetic overflow")
	// ErrNegative means a value required to be non-negative was negative.
	ErrNegative = errors.New("clock: negative time value")
)

// ToNanos converts a tick count at ticksPerSecond into canonical nanoseconds.
// It rejects non-positive tick rates and overflowing results.
func ToNanos(ticks, ticksPerSecond int64) (int64, error) {
	if ticksPerSecond <= 0 {
		return 0, ErrInvalidTick
	}
	// nanos = ticks * 1e9 / rate; multiply first with an overflow guard.
	if ticks > 0 && ticks > MaxNanos/NanosecondsPerSecond {
		return 0, ErrOverflow
	}
	if ticks < 0 && ticks < MinNanos/NanosecondsPerSecond {
		return 0, ErrOverflow
	}
	scaled := ticks * NanosecondsPerSecond
	return scaled / ticksPerSecond, nil
}

// FromNanos converts canonical nanoseconds into a tick count at
// ticksPerSecond, rounding to the nearest tick: ticks = nanos*rate/1e9.
func FromNanos(nanos, ticksPerSecond int64) (int64, error) {
	if ticksPerSecond <= 0 {
		return 0, ErrInvalidTick
	}
	// Guard the nanos*rate multiplication before rounding adjustment.
	if nanos > 0 && nanos > MaxNanos/ticksPerSecond {
		return 0, ErrOverflow
	}
	if nanos < 0 && nanos < MinNanos/ticksPerSecond {
		return 0, ErrOverflow
	}
	scaled := nanos * ticksPerSecond
	half := NanosecondsPerSecond / 2
	if scaled >= 0 {
		if scaled > MaxNanos-half {
			return 0, ErrOverflow
		}
		return (scaled + half) / NanosecondsPerSecond, nil
	}
	if scaled < MinNanos+half {
		return 0, ErrOverflow
	}
	return (scaled - half) / NanosecondsPerSecond, nil
}

// Add returns nanos+d and rejects overflow.
func Add(nanos, d int64) (int64, error) {
	if d > 0 && nanos > MaxNanos-d {
		return 0, ErrOverflow
	}
	if d < 0 && nanos < MinNanos-d {
		return 0, ErrOverflow
	}
	return nanos + d, nil
}

// Sub returns nanos-base and rejects overflow.
func Sub(nanos, base int64) (int64, error) {
	if base > 0 && nanos < MinNanos+base {
		return 0, ErrOverflow
	}
	if base < 0 && nanos > MaxNanos+base {
		return 0, ErrOverflow
	}
	return nanos - base, nil
}

// CheckMonotonic validates that next is not earlier than prev. A backward
// timestamp is rejected rather than silently reordered.
func CheckMonotonic(prev, next int64) error {
	if next < prev {
		return errors.New("clock: timestamp went backwards")
	}
	return nil
}
