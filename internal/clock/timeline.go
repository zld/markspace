package clock

import (
	"fmt"
	"math/bits"
)

// Rational is an exact non-negative time value n/d seconds. It is deliberately
// not normalized through floats: two Rationals compare by cross products so a
// long programme never accumulates rounding drift.
//
// A Rational is safe for concurrent reads once built. Construct values through
// Make or the zero value.
type Rational struct {
	N int64 // numerator, ticks
	D int64 // denominator, ticks per second (a timescale)
}

// Make returns n/d, reducing by the greatest common divisor to keep cross
// products small.
func Make(n, d int64) (Rational, error) {
	if d <= 0 {
		return Rational{}, fmt.Errorf("%w: denominator %d", ErrOverflow, d)
	}
	if n < 0 {
		return Rational{}, fmt.Errorf("%w: negative time %d/%d", ErrOverflow, n, d)
	}
	g := gcd(n, d)
	if g > 1 {
		n /= g
		d /= g
	}
	return Rational{N: n, D: d}, nil
}

// MustMake is Make for compile-time constants and tests.
func MustMake(n, d int64) Rational {
	r, err := Make(n, d)
	if err != nil {
		panic(err)
	}
	return r
}

// Zero is the zero time.
func Zero() Rational { return Rational{N: 0, D: 1} }

// FromTicks converts a raw sample count on a track timescale.
func FromTicks(ticks uint64, timescale uint32) (Rational, error) {
	if ticks > uint64(1<<63-1) {
		return Rational{}, fmt.Errorf("%w: ticks %d", ErrOverflow, ticks)
	}
	if timescale == 0 {
		return Rational{}, fmt.Errorf("%w: zero timescale", ErrOverflow)
	}
	return Make(int64(ticks), int64(timescale))
}

// TicksExpr returns the exact number of ticks on denominator d. The result is
// rounded half up, matching how a fixed-point timescale is normally emitted.
func (r Rational) TicksExpr(d int64) (int64, error) {
	if d <= 0 {
		return 0, fmt.Errorf("%w: denominator %d", ErrOverflow, d)
	}
	hi, lo := bits.Mul64(uint64(r.N), uint64(d))
	if hi != 0 || lo > (1<<63-1) {
		return 0, fmt.Errorf("%w: %d/%d to %d", ErrOverflow, r.N, r.D, d)
	}
	q := int64(lo) / r.D
	if rem := int64(lo) % r.D; rem*2 >= r.D {
		q++
	}
	return q, nil
}

// Cmp compares a and b exactly.
func (r Rational) Cmp(o Rational) int {
	aHi, aLo := bits.Mul64(uint64(r.N), uint64(o.D))
	bHi, bLo := bits.Mul64(uint64(o.N), uint64(r.D))
	switch {
	case aHi != bHi:
		if aHi < bHi {
			return -1
		}
		return 1
	case aLo != bLo:
		if aLo < bLo {
			return -1
		}
		return 1
	default:
		return 0
	}
}

// Add returns r+o exactly. Overflow is reported rather than saturating.
func (r Rational) Add(o Rational) (Rational, error) {
	hi, lo := bits.Mul64(uint64(r.N), uint64(o.D))
	if hi != 0 {
		return Rational{}, ErrOverflow
	}
	hi2, lo2 := bits.Mul64(uint64(o.N), uint64(r.D))
	if hi2 != 0 {
		return Rational{}, ErrOverflow
	}
	sum, carry := bits.Add64(lo, lo2, 0)
	if carry != 0 || sum > (1<<63-1) {
		return Rational{}, ErrOverflow
	}
	den, err := mul64(r.D, o.D)
	if err != nil {
		return Rational{}, err
	}
	return Make(int64(sum), den)
}

// Sub returns r-o, or an error if the result would be negative.
func (r Rational) Sub(o Rational) (Rational, error) {
	if r.Cmp(o) < 0 {
		return Rational{}, fmt.Errorf("%w: %s - %s is negative", ErrOverflow, r, o)
	}
	hi, lo := bits.Mul64(uint64(r.N), uint64(o.D))
	if hi != 0 {
		return Rational{}, ErrOverflow
	}
	hi2, lo2 := bits.Mul64(uint64(o.N), uint64(r.D))
	if hi2 != 0 || lo < lo2 {
		return Rational{}, ErrOverflow
	}
	den, err := mul64(r.D, o.D)
	if err != nil {
		return Rational{}, err
	}
	return Make(int64(lo-lo2), den)
}

func mul64(a, b int64) (int64, error) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > (1<<63-1) {
		return 0, ErrOverflow
	}
	return int64(lo), nil
}

func (r Rational) String() string {
	if r.D == 0 {
		return "0s"
	}
	return fmt.Sprintf("%d/%ds", r.N, r.D)
}

// Milliseconds renders the value as a float for human-readable reports only.
func (r Rational) Milliseconds() float64 {
	return float64(r.N) * 1000 / float64(r.D)
}

// Seconds is provided for reporting only; never use it for comparisons.
func (r Rational) Seconds() float64 {
	return float64(r.N) / float64(r.D)
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
