package media

import (
	"errors"
	"math/bits"
)

// NanosPerSecond is the common wall-clock timebase used for reporting.
const NanosPerSecond uint32 = 1_000_000_000

var (
	// ErrInvalidTimescale is returned for a zero or otherwise unusable timebase.
	ErrInvalidTimescale = errors.New("media: invalid timescale")
	// ErrTimestampOverflow is returned when a rescale would not fit in uint64.
	ErrTimestampOverflow = errors.New("media: timestamp overflow")
)

// Timebase couples a media timescale with a duration expressed in ticks of
// that timescale. All conversions are integer based and overflow checked.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
	Duration  uint64 `json:"duration"`
}

// NewTimebase validates and constructs a Timebase.
func NewTimebase(timescale uint32, duration uint64) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, ErrInvalidTimescale
	}
	return Timebase{Timescale: timescale, Duration: duration}, nil
}

// Valid reports whether the timebase can be used for conversions.
func (tb Timebase) Valid() bool { return tb.Timescale != 0 }

// Seconds converts ticks to seconds as a float for display only.
func (tb Timebase) Seconds(ticks uint64) float64 {
	if tb.Timescale == 0 {
		return 0
	}
	return float64(ticks) / float64(tb.Timescale)
}

// Nanoseconds converts ticks to nanoseconds with overflow checking.
func (tb Timebase) Nanoseconds(ticks uint64) (uint64, error) {
	if !tb.Valid() {
		return 0, ErrInvalidTimescale
	}
	return Rescale(ticks, tb.Timescale, NanosPerSecond)
}

// FromNanoseconds converts nanoseconds into ticks of this timebase.
func (tb Timebase) FromNanoseconds(ns uint64) (uint64, error) {
	if !tb.Valid() {
		return 0, ErrInvalidTimescale
	}
	return Rescale(ns, NanosPerSecond, tb.Timescale)
}

// Add returns a+b in ticks, rejecting overflow.
func (tb Timebase) Add(a, b uint64) (uint64, error) {
	sum := a + b
	if sum < a {
		return 0, ErrTimestampOverflow
	}
	return sum, nil
}

// Rescale converts value from src ticks to dst ticks using floor division.
// The multiplication is performed with a 128-bit intermediate.
func Rescale(value uint64, src, dst uint32) (uint64, error) {
	if src == 0 || dst == 0 {
		return 0, ErrInvalidTimescale
	}
	if src == dst {
		return value, nil
	}
	hi, lo := bits.Mul64(value, uint64(dst))
	if hi >= uint64(src) {
		return 0, ErrTimestampOverflow
	}
	q, _ := bits.Div64(hi, lo, uint64(src))
	return q, nil
}

// RescaleRound converts value from src ticks to dst ticks using nearest
// rounding (half away from zero).
func RescaleRound(value uint64, src, dst uint32) (uint64, error) {
	if src == 0 || dst == 0 {
		return 0, ErrInvalidTimescale
	}
	if src == dst {
		return value, nil
	}
	hi, lo := bits.Mul64(value, uint64(dst))
	if hi >= uint64(src) {
		return 0, ErrTimestampOverflow
	}
	q, r := bits.Div64(hi, lo, uint64(src))
	if r >= (uint64(src)+1)/2 {
		if q == ^uint64(0) {
			return 0, ErrTimestampOverflow
		}
		q++
	}
	return q, nil
}

// ConvertDuration rescales a duration between two timebases.
func ConvertDuration(duration uint64, src, dst Timebase) (uint64, error) {
	return RescaleRound(duration, src.Timescale, dst.Timescale)
}
