// Package clock implements the rational time bases, PCR/PTS wrap expansion,
// drift estimation and VFR-aware sample timing used by the TS repair kernel.
package clock

import (
	"errors"
	"math/big"

	"tsrepair/internal/mediaerr"
)

// Standard MPEG-TS clock rates.
const (
	// PCRRate is the 27 MHz system clock used by PCR fields.
	PCRRate int64 = 27_000_000
	// PTSRate is the 90 kHz clock used by PTS/DTS fields.
	PTSRate int64 = 90_000
)

// Rational is an exact rational number num/den, den > 0.
// All time-base arithmetic goes through this type so that conversions between
// the 27 MHz PCR, 90 kHz PTS and nanosecond wall clocks never accumulate
// floating point error.
type Rational struct {
	Num int64
	Den int64 // always positive
}

// NewRational constructs a rational, normalising the denominator sign.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("rational: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	return Rational{Num: num, Den: den}, nil
}

// MulScale computes value * num / den with overflow detection, returning a
// mediaerr TIME_OVERFLOW-style error when the result does not fit int64.
func MulScale(value, num, den int64) (int64, error) {
	if den == 0 {
		return 0, errors.New("clock: zero scale denominator")
	}
	// Use big.Int for the intermediate product to guarantee detection.
	var v, n, d big.Int
	v.SetInt64(value)
	n.SetInt64(num)
	d.SetInt64(den)
	var q big.Int
	q.Mul(&v, &n).Quo(&q, &d)
	if !q.IsInt64() {
		return 0, mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0, 0,
			"time arithmetic overflow", "mark affected samples unindexable")
	}
	return q.Int64(), nil
}

// TicksToNanos converts ticks counted at rate ticksPerSecond into nanoseconds,
// rejecting overflow.
func TicksToNanos(ticks, ticksPerSecond int64) (int64, error) {
	return MulScale(ticks, 1_000_000_000, ticksPerSecond)
}

// NanosToTicks converts nanoseconds into ticks at the given rate.
func NanosToTicks(nanos, ticksPerSecond int64) (int64, error) {
	return MulScale(nanos, ticksPerSecond, 1_000_000_000)
}

// AbsDiff returns |a-b| or an error on overflow (only possible at the int64
// extremes).
func AbsDiff(a, b int64) (int64, error) {
	d := a - b
	// Detect the two overflow cases.
	if (a < 0) != (b < 0) {
		if d >= 0 && a < 0 {
			return 0, mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0, 0,
				"time difference overflow", "mark affected samples unindexable")
		}
	}
	if d < 0 {
		d = -d
		if d < 0 {
			return 0, mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0, 0,
				"time difference overflow", "mark affected samples unindexable")
		}
	}
	return d, nil
}

// CheckedAdd adds two int64 tick values with overflow detection.
func CheckedAdd(a, b int64) (int64, error) {
	r := a + b
	if (a > 0 && b > 0 && r < 0) || (a < 0 && b < 0 && r > 0) {
		return 0, mediaerr.Fatal(mediaerr.CodeTimeOverflow, 0, 0,
			"time addition overflow", "mark affected samples unindexable")
	}
	return r, nil
}

// Nanos returns the rational value expressed in nanoseconds.
func (r Rational) Nanos() (int64, error) {
	return MulScale(r.Num, 1_000_000_000, r.Den)
}
