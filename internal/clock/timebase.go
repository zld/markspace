// Package clock defines the rational media time base shared by every
// module. Time bases are expressed as ticks per second (e.g. 90000/1 for the
// RTP media clock); all conversions are exact rational operations with
// explicit overflow errors so a corrupted or out-of-range timestamp can never
// wrap silently into a valid frame position.
package clock

import (
	"errors"
	"math"
	"math/big"
)

// Rational represents the exact ratio Num/Den. A valid Rational always has a
// positive denominator. For time bases the ratio means ticks per second.
type Rational struct {
	Num int64
	Den int64
}

var (
	// ErrInvalidRational is returned when a denominator is zero or a Rational
	// in an invalid state is used.
	ErrInvalidRational = errors.New("clock: rational denominator must be non-zero")
	// ErrOverflow is returned when a conversion result does not fit the
	// target integer type.
	ErrOverflow = errors.New("clock: time conversion overflow")
)

// MediaClock is the RTP media clock shared by all tracks: 90000 ticks/s.
var MediaClock = Rational{Num: 90000, Den: 1}

// NanosPerSecond is the number of nanoseconds in one second.
const NanosPerSecond int64 = 1_000_000_000

// New validates and normalizes a Rational, moving any sign onto Num.
func New(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrInvalidRational
	}
	if den < 0 {
		if num == math.MinInt64 || den == math.MinInt64 {
			return Rational{}, ErrOverflow
		}
		num, den = -num, -den
	}
	return Rational{Num: num, Den: den}, nil
}

// Valid reports whether r is usable for conversions.
func (r Rational) Valid() bool { return r.Den > 0 }

func fitInt64(v *big.Int) (int64, error) {
	if v.IsInt64() {
		return v.Int64(), nil
	}
	return 0, ErrOverflow
}

// mulDiv computes value * mulNum / divDen with truncation toward zero, using
// big.Int intermediates so ordinary media values never hit an int64
// intermediate overflow.
func mulDiv(value, mulNum, divDen int64) (int64, error) {
	if divDen == 0 {
		return 0, ErrInvalidRational
	}
	v := new(big.Int).Mul(big.NewInt(value), big.NewInt(mulNum))
	v.Quo(v, big.NewInt(divDen))
	return fitInt64(v)
}

// NanosToTicks converts a nanosecond capture time onto the ticks-per-second
// time base tb: ticks = nanos * tb.Num / (tb.Den * 1e9).
func NanosToTicks(nanos int64, tb Rational) (int64, error) {
	if !tb.Valid() {
		return 0, ErrInvalidRational
	}
	return mulDiv(nanos, tb.Num, tb.Den*NanosPerSecond)
}

// TicksToNanos converts ticks on a ticks-per-second time base back to
// nanoseconds: nanos = ticks * tb.Den * 1e9 / tb.Num.
func TicksToNanos(ticks int64, tb Rational) (int64, error) {
	if !tb.Valid() {
		return 0, ErrInvalidRational
	}
	return mulDiv(ticks, tb.Den*NanosPerSecond, tb.Num)
}

// Rescale moves a tick count from one ticks-per-second time base to another.
func Rescale(ticks int64, from, to Rational) (int64, error) {
	if !from.Valid() || !to.Valid() {
		return 0, ErrInvalidRational
	}
	v := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(to.Num))
	v.Mul(v, big.NewInt(from.Den))
	v.Quo(v, new(big.Int).Mul(big.NewInt(from.Num), big.NewInt(to.Den)))
	return fitInt64(v)
}

// RTPTimestamp maps a nanosecond capture time to a uint32 RTP timestamp on
// tb. A value outside the uint32 range is a time overflow failure rather than
// a wraparound.
func RTPTimestamp(nanos int64, tb Rational) (uint32, error) {
	ticks, err := NanosToTicks(nanos, tb)
	if err != nil {
		return 0, err
	}
	if ticks < 0 || ticks > math.MaxUint32 {
		return 0, ErrOverflow
	}
	return uint32(ticks), nil
}
