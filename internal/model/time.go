// Package model defines the shared integer-nanosecond media time model and
// rational-number conversions used by every other package.
package model

import (
	"math"
	"math/bits"
)

// Nano is the canonical media time unit: an integer number of nanoseconds.
// All timelines are expressed in Nano values; per-track native time bases are
// preserved alongside the converted values.
type Nano int64

// Nanosecond is one Nano unit.
const Nanosecond Nano = 1

// Microsecond, Millisecond and Second are Nano scale constants.
const (
	Microsecond Nano = 1_000
	Millisecond Nano = 1_000_000
	Second      Nano = 1_000_000_000
)

// Rational is a signed integer ratio Num/Den (Den is always positive).
// Conversions never use floating point so results are deterministic.
type Rational struct {
	Num int64
	Den int64
}

// NewRational builds a Rational and normalizes the denominator to positive.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrTimeOverflow.At("time", 0, "", 0,
			"zero denominator in rational")
	}
	if den < 0 {
		num, den = -num, -den
	}
	return Rational{Num: num, Den: den}, nil
}

// mul64 returns the 128-bit product a*b as (hi, lo).
func mul64(a, b uint64) (hi, lo uint64) { return bits.Mul64(a, b) }

// fitsInt64 reports whether the unsigned 128-bit value (hi,lo) fits int64.
func fitsInt64(hi, lo uint64, negative bool) bool {
	if negative {
		// Absolute value of math.MinInt64 is 1<<63; any greater abs overflows.
		if hi == 0 && lo <= uint64(math.MaxInt64)+1 {
			return true
		}
		return false
	}
	return hi == 0 && lo <= uint64(math.MaxInt64)
}

// checkedMulInt computes a*b with signed 128-bit overflow detection.
func checkedMulInt(a, b int64) (int64, bool) {
	neg := (a < 0) != (b < 0)
	ua, ub := uint64(a), uint64(b)
	if a < 0 {
		ua = uint64(-a)
	}
	if b < 0 {
		ub = uint64(-b)
	}
	hi, lo := mul64(ua, ub)
	if !fitsInt64(hi, lo, neg) {
		return 0, false
	}
	v := int64(lo)
	if neg {
		v = -v
	}
	return v, true
}

// Scale converts value*num/den into an integer, rounded toward zero. It returns
// an error instead of silently wrapping when the result overflows int64.
func Scale(value, num, den int64) (int64, error) {
	if den == 0 {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"zero denominator in Scale")
	}
	p, ok := checkedMulInt(value, num)
	if !ok {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"product overflows int64")
	}
	return p / den, nil
}

// ToNano converts ticks counted in rate (units per second) to integer
// nanoseconds. Negative rates are rejected; overflow returns a locate-able error.
func ToNano(ticks int64, rate uint32) (Nano, error) {
	if rate == 0 {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"zero time-base rate")
	}
	v, err := Scale(ticks, int64(Second), int64(rate))
	if err != nil {
		return 0, err
	}
	return Nano(v), nil
}

// FromNano converts integer nanoseconds back to ticks of the given rate,
// rounded toward zero.
func FromNano(t Nano, rate uint32) (int64, error) {
	if rate == 0 {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"zero time-base rate")
	}
	return Scale(int64(t), int64(rate), int64(Second))
}

// RescaleNano expresses a Nano value as ticks of rate, rounded to nearest with
// halves rounded away from zero; used when emitting native time bases.
func RescaleNano(t Nano, rate uint32) (int64, error) {
	if rate == 0 {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"zero time-base rate")
	}
	num, ok := checkedMulInt(int64(t), int64(rate))
	if !ok {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"rescale product overflows int64")
	}
	den := int64(Second)
	if num >= 0 {
		return (num + den/2) / den, nil
	}
	return -((-num + den/2) / den), nil
}

// AddNano adds two Nano values, rejecting int64 overflow.
func AddNano(a, b Nano) (Nano, error) {
	r := a + b
	if (a > 0 && b > 0 && r < 0) || (a < 0 && b < 0 && r > 0) {
		return 0, ErrTimeOverflow.At("time", 0, "", 0,
			"nanosecond addition overflows int64")
	}
	return r, nil
}
