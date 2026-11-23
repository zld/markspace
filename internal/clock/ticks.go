// Package clock provides exact rational time arithmetic for media timelines.
//
// All timestamps in this library are integer tick counts on a rational rate
// (ticks per second).  Floating point is never used to accumulate time, so
// long media cannot drift.  Conversions round half away from zero and the
// streaming Accumulator carries the exact remainder forward, keeping the
// cumulative error strictly below one destination tick.
package clock

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by this package.
var (
	ErrInvalidRate = errors.New("clock: rate numerator and denominator must be positive")
	ErrOverflow    = errors.New("clock: tick arithmetic overflow")
	ErrNegative    = errors.New("clock: negative tick value")
)

// Rate is a rational tick rate such as 44100/1 or 90000/1.
// It is always kept reduced and with a positive denominator.
type Rate struct {
	Num int64
	Den int64
}

// NewRate validates and reduces a rational rate.
func NewRate(num, den int64) (Rate, error) {
	if num <= 0 || den <= 0 {
		return Rate{}, ErrInvalidRate
	}
	g := gcd(num, den)
	return Rate{Num: num / g, Den: den / g}, nil
}

// MustRate is NewRate for compile-time constants and panics on error.
func MustRate(num, den int64) Rate {
	r, err := NewRate(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Common media rates.
var (
	Rate44100 = MustRate(44100, 1)
	Rate48000 = MustRate(48000, 1)
	Rate90000 = MustRate(90000, 1)
	RateMilli = MustRate(1000, 1)
)

// IsValid reports whether r can be used for tick arithmetic.
func (r Rate) IsValid() bool { return r.Num > 0 && r.Den > 0 }

// String renders the rate as "num/den".
func (r Rate) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%d", r.Num)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// Rescale converts n ticks at rate from into ticks at rate to, rounding half
// away from zero.  n must be non-negative.
func Rescale(n int64, from, to Rate) (int64, error) {
	if !from.IsValid() || !to.IsValid() {
		return 0, ErrInvalidRate
	}
	if n < 0 {
		return 0, ErrNegative
	}
	// exact ticks = n * to.Num * from.Den / (from.Num * to.Den)
	num, err := mulChecked(n, to.Num)
	if err != nil {
		return 0, err
	}
	num, err = mulChecked(num, from.Den)
	if err != nil {
		return 0, err
	}
	den, err := mulChecked(from.Num, to.Den)
	if err != nil {
		return 0, err
	}
	return roundDiv(num, den), nil
}

// RescaleFloor is like Rescale but truncates toward zero.  It is used where
// an interval must never be reported longer than it really is.
func RescaleFloor(n int64, from, to Rate) (int64, error) {
	if !from.IsValid() || !to.IsValid() {
		return 0, ErrInvalidRate
	}
	if n < 0 {
		return 0, ErrNegative
	}
	num, err := mulChecked(n, to.Num)
	if err != nil {
		return 0, err
	}
	num, err = mulChecked(num, from.Den)
	if err != nil {
		return 0, err
	}
	den, err := mulChecked(from.Num, to.Den)
	if err != nil {
		return 0, err
	}
	return num / den, nil
}

// Accumulator converts a stream of tick counts without losing sub-tick
// remainders.  Every call is O(1); the carried remainder is always a proper
// fraction, so the cumulative difference from exact rational conversion stays
// strictly below one destination tick.
type Accumulator struct {
	From  Rate
	To    Rate
	pnum  int64 // reduced numerator of the conversion ratio
	pden  int64 // reduced denominator of the conversion ratio
	carry int64 // remainder numerator, always 0 <= carry < pden
}

// NewAccumulator builds a streaming converter from one rate to another.
func NewAccumulator(from, to Rate) (*Accumulator, error) {
	if !from.IsValid() || !to.IsValid() {
		return nil, ErrInvalidRate
	}
	// ratio = to.Num*from.Den / (from.Num*to.Den)
	pnum, err := mulChecked(to.Num, from.Den)
	if err != nil {
		return nil, err
	}
	pden, err := mulChecked(from.Num, to.Den)
	if err != nil {
		return nil, err
	}
	g := gcd(pnum, pden)
	return &Accumulator{From: from, To: to, pnum: pnum / g, pden: pden / g}, nil
}

// Reset clears the carried remainder.
func (a *Accumulator) Reset() { a.carry = 0 }

// Convert consumes n source ticks and returns the number of whole destination
// ticks completed so far.  The result is a floor, so summing outputs never
// exceeds the exact elapsed duration.  n must be non-negative.
func (a *Accumulator) Convert(n int64) (int64, error) {
	if n < 0 {
		return 0, ErrNegative
	}
	num, err := mulChecked(n, a.pnum)
	if err != nil {
		return 0, err
	}
	if num > 0 && a.carry > maxInt64-num {
		return 0, ErrOverflow
	}
	num += a.carry
	q := num / a.pden
	a.carry = num % a.pden
	return q, nil
}

// ErrorTicks returns the currently carried error as a fraction of one
// destination tick.  It is always in [0, 1).
func (a *Accumulator) ErrorTicks() float64 {
	if a.pden == 0 {
		return 0
	}
	return float64(a.carry) / float64(a.pden)
}

// ErrorTicksExact returns the carried error as an exact fraction (carry over
// pden), useful in tests and diagnostics without floating point.
func (a *Accumulator) ErrorTicksExact() (carry, den int64) {
	return a.carry, a.pden
}

// SampleDuration returns the exact duration of n samples at rate r.
func SampleDuration(samples int64, r Rate) (int64, error) {
	if !r.IsValid() {
		return 0, ErrInvalidRate
	}
	if samples < 0 {
		return 0, ErrNegative
	}
	// n samples at r ticks/second last n/r seconds = n*1 ticks at rate r.
	// The tick count is simply n.
	return samples, nil
}

// SecondsToTicks converts a duration in whole milliseconds to ticks at r,
// rounding half away from zero.
func MillisecondsToTicks(ms int64, r Rate) (int64, error) {
	return Rescale(ms, RateMilli, r)
}

// TicksToMilliseconds converts ticks at r to whole milliseconds rounded half
// away from zero.
func TicksToMilliseconds(ticks int64, r Rate) (int64, error) {
	return Rescale(ticks, r, RateMilli)
}

// TicksToMillisecondsExact returns a float millisecond value for reporting
// only, never for accumulation.
func TicksToMillisecondsExact(ticks int64, r Rate) float64 {
	if !r.IsValid() {
		return 0
	}
	return float64(ticks) * 1000 * float64(r.Den) / float64(r.Num)
}

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

const maxInt64 = int64(^uint64(0) >> 1)

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a < 0 || b < 0 {
		return 0, ErrNegative
	}
	if a > maxInt64/b {
		return 0, ErrOverflow
	}
	return a * b, nil
}

// roundDiv divides num by a positive den, rounding half away from zero.
func roundDiv(num, den int64) int64 {
	q := num / den
	r := num % den
	if r >= 0 {
		if r >= den-r {
			q++
		}
		return q
	}
	if -r >= den+r {
		q--
	}
	return q
}
