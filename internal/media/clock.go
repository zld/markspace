package media

import (
	"math"
	"math/bits"
)

// Nano is the pipeline-wide integer time base: nanoseconds.
//
// Every track keeps its original sample values and timescale; conversion to
// Nano is done exactly (rational scaling with deterministic round-half-away
// from zero) so that comparisons across tracks and representations never
// depend on float64 precision.
type Nano int64

// Timebase converts sample times expressed in a track timescale to Nano.
type Timebase struct {
	scale uint32
}

// NewTimebase validates a timescale. A zero timescale makes every timestamp
// meaningless, so it is rejected at construction.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, NewError(ZeroTimescale, "timescale must be non-zero")
	}
	return Timebase{scale: timescale}, nil
}

// Timescale returns the underlying ticks-per-second value.
func (tb Timebase) Timescale() uint32 { return tb.scale }

// Nanos converts a timestamp in this timebase to nanoseconds.
func (tb Timebase) Nanos(value int64) (Nano, error) {
	if tb.scale == 0 {
		return 0, NewError(ZeroTimescale, "timescale must be non-zero")
	}
	return ToNanos(value, tb.scale)
}

// Duration converts a non-negative duration in this timebase to nanoseconds.
// Negative durations are rejected: they indicate a corrupt sample table.
func (tb Timebase) Duration(value int64) (Nano, error) {
	if value < 0 {
		return 0, NewError(NegativeDuration, "duration %d is negative", value)
	}
	return tb.Nanos(value)
}

// ToNanos converts value/timescale seconds to nanoseconds, detecting overflow.
// The result may be negative (an edit or composition offset) but must fit in
// int64.
func ToNanos(value int64, timescale uint32) (Nano, error) {
	if timescale == 0 {
		return 0, NewError(ZeroTimescale, "timescale must be non-zero")
	}
	neg := value < 0
	var mag uint64
	if neg {
		mag = uint64(-(value + 1)) + 1
	} else {
		mag = uint64(value)
	}
	hi, lo := bits.Mul64(mag, 1_000_000_000)
	if hi >= uint64(timescale) {
		return 0, NewError(Overflow,
			"converting %d ticks at timescale %d to nanoseconds overflows int64",
			value, timescale)
	}
	q, r := bits.Div64(hi, lo, uint64(timescale))
	if r*2 >= uint64(timescale) {
		q++
	}
	const maxPositive = uint64(math.MaxInt64)
	const maxNegative = maxPositive + 1 // magnitude of math.MinInt64
	if (!neg && q > maxPositive) || (neg && q > maxNegative) {
		return 0, NewError(Overflow,
			"converting %d ticks at timescale %d to nanoseconds overflows int64",
			value, timescale)
	}
	if neg {
		return Nano(-int64(q)), nil
	}
	return Nano(int64(q)), nil
}

// ToTicks converts a nanosecond duration to ticks in timescale. The result is
// rounded half away from zero and must fit in int64.
func ToTicks(nanos Nano, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, NewError(ZeroTimescale, "timescale must be non-zero")
	}
	neg := nanos < 0
	v := int64(nanos)
	var mag uint64
	if neg {
		mag = uint64(-(v + 1)) + 1
	} else {
		mag = uint64(v)
	}
	hi, lo := bits.Mul64(mag, uint64(timescale))
	if hi >= 1_000_000_000 {
		return 0, NewError(Overflow,
			"converting %d ns at timescale %d to ticks overflows int64",
			int64(nanos), timescale)
	}
	q, r := bits.Div64(hi, lo, 1_000_000_000)
	if r*2 >= 1_000_000_000 {
		q++
	}
	if q > uint64(math.MaxInt64) {
		return 0, NewError(Overflow,
			"converting %d ns at timescale %d to ticks overflows int64",
			int64(nanos), timescale)
	}
	if neg {
		return -int64(q), nil
	}
	return int64(q), nil
}

// Add returns a+b, failing instead of wrapping.
func Add(a, b Nano) (Nano, error) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, NewError(Overflow, "adding %d ns to %d ns overflows", int64(b), int64(a))
	}
	return sum, nil
}

// Sub returns a-b, failing instead of wrapping.
func Sub(a, b Nano) (Nano, error) {
	diff := a - b
	if (b < 0 && diff < a) || (b > 0 && diff > a) {
		return 0, NewError(Overflow, "subtracting %d ns from %d ns overflows", int64(b), int64(a))
	}
	return diff, nil
}

// Millis formats a duration in milliseconds with microsecond resolution.
func Millis(d Nano) float64 { return float64(d) / 1e6 }
