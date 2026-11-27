package rtpreplay

import (
	"fmt"
	"math/bits"
)

// Timebase is a rational sample clock: times are expressed as an integer tick
// count over a positive rate. All conversions use exact 128-bit integer
// arithmetic; a conversion that would overflow returns CodeTimeOverflow
// instead of wrapping or accumulating float error.
type Timebase struct {
	Rate uint32
}

// NewTimebase validates and constructs a timebase.
func NewTimebase(rate uint32) (Timebase, error) {
	if rate == 0 {
		return Timebase{}, NewError(CodeInvalidConfig, "timebase", "rate must be positive")
	}
	return Timebase{Rate: rate}, nil
}

// Valid reports whether the timebase has a usable rate.
func (tb Timebase) Valid() bool { return tb.Rate != 0 }

// String renders the timebase in the report-friendly "num/den" form.
func (tb Timebase) String() string { return fmt.Sprintf("%d/1", tb.Rate) }

// mulDiv computes (a*b)/d with a 128-bit intermediate. It returns an error if
// a*b does not fit in 128 bits or if the quotient exceeds 64 bits. All inputs
// are non-negative (timestamps are unsigned).
func mulDiv(a, b uint64, d uint32, what string) (uint64, error) {
	if d == 0 {
		return 0, NewError(CodeTimeOverflow, "mulDiv", what+": zero denominator")
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= uint64(d) {
		return 0, NewError(CodeTimeOverflow, "mulDiv", fmt.Sprintf("%s: %d*%d/%d exceeds 64 bits", what, a, b, d))
	}
	q, _ := bits.Div64(hi, lo, uint64(d))
	return q, nil
}

// Scale converts a count from one rate to another. Conversion truncates
// toward zero; callers that need sub-tick precision aggregate in the source
// rate before scaling.
func Scale(ticks uint64, from, to uint32) (uint64, error) {
	if from == 0 || to == 0 {
		return 0, NewError(CodeTimeOverflow, "scale", "zero rate")
	}
	if from == to {
		return ticks, nil
	}
	return mulDiv(ticks, uint64(to), from, "scale")
}

// TicksToNanos converts ticks at rate into integer nanoseconds.
func TicksToNanos(ticks uint64, rate uint32) (int64, error) {
	if rate == 0 {
		return 0, NewError(CodeTimeOverflow, "ticksToNanos", "zero rate")
	}
	v, err := mulDiv(ticks, 1_000_000_000, rate, "ticksToNanos")
	if err != nil {
		return 0, err
	}
	if v > 1<<63-1 {
		return 0, NewError(CodeTimeOverflow, "ticksToNanos", "nanoseconds exceed int64")
	}
	return int64(v), nil
}

// NanosToTicks converts integer nanoseconds into ticks at rate, truncating.
func NanosToTicks(ns int64, rate uint32) (uint64, error) {
	if ns < 0 {
		return 0, NewError(CodeInvalidInput, "nanosToTicks", "negative nanoseconds")
	}
	return mulDiv(uint64(ns), uint64(rate), 1_000_000_000, "nanosToTicks")
}

// DurationTicks returns the duration in ticks for a millisecond value.
func DurationTicks(ms uint64, rate uint32) (uint64, error) {
	return mulDiv(ms, uint64(rate), 1000, "durationMsToTicks")
}

// Millis converts ticks to whole milliseconds (truncating), for reporting.
func Millis(ticks uint64, rate uint32) (int64, error) {
	if rate == 0 {
		return 0, NewError(CodeTimeOverflow, "millis", "zero rate")
	}
	v, err := mulDiv(ticks, 1000, rate, "millis")
	if err != nil {
		return 0, err
	}
	if v > 1<<63-1 {
		return 0, NewError(CodeTimeOverflow, "millis", "milliseconds exceed int64")
	}
	return int64(v), nil
}

// MillisF converts ticks to milliseconds as a float only at the reporting
// boundary. Metric math never uses this value for accumulation.
func MillisF(ticks uint64, rate uint32) float64 {
	return float64(ticks) * 1000 / float64(rate)
}
