package media

import (
	"fmt"
	"math"
)

// Rational is an exact time value in seconds: Num/Den. Den is always > 0
// and the fraction is kept in lowest terms so equal times compare equal.
type Rational struct {
	Num int64
	Den int64
}

// Zero is the rational zero time.
var Zero = Rational{Num: 0, Den: 1}

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
	if a == 0 {
		return 1
	}
	return a
}

// NewRational normalises num/den. A zero denominator is treated as one so
// malformed input cannot produce a panic; callers that care validate first.
func NewRational(num, den int64) Rational {
	if den == 0 {
		den = 1
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(num, den)
	return Rational{Num: num / g, Den: den / g}
}

// FromTicks converts a media tick count at the given timescale to seconds.
func FromTicks(ticks int64, timescale uint32) Rational {
	if timescale == 0 {
		return Zero
	}
	return NewRational(ticks, int64(timescale))
}

// FromMicros converts integer microseconds to an exact rational.
func FromMicros(us int64) Rational {
	return NewRational(us, 1_000_000)
}

// FromMillis converts integer milliseconds to an exact rational.
func FromMillis(ms int64) Rational {
	return NewRational(ms, 1_000)
}

func (r Rational) IsZero() bool { return r.Num == 0 }

func (r Rational) Sign() int {
	switch {
	case r.Num < 0:
		return -1
	case r.Num > 0:
		return 1
	default:
		return 0
	}
}

func (r Rational) Neg() Rational { return Rational{Num: -r.Num, Den: r.Den} }

func (r Rational) Add(o Rational) Rational {
	return NewRational(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den)
}

func (r Rational) Sub(o Rational) Rational {
	return NewRational(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den)
}

func (r Rational) MulInt(n int64) Rational {
	return NewRational(r.Num*n, r.Den)
}

// Scale converts to a tick count at timescale, rounding to nearest with
// halves away from zero.
func (r Rational) Scale(timescale uint32) int64 {
	return divRound(r.Num*int64(timescale), r.Den)
}

func divRound(num, den int64) int64 {
	if den < 0 {
		num, den = -num, -den
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

func (r Rational) Cmp(o Rational) int {
	l := r.Num * o.Den
	rr := o.Num * r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

func (r Rational) Less(o Rational) bool      { return r.Cmp(o) < 0 }
func (r Rational) LessEq(o Rational) bool    { return r.Cmp(o) <= 0 }
func (r Rational) Greater(o Rational) bool   { return r.Cmp(o) > 0 }
func (r Rational) GreaterEq(o Rational) bool { return r.Cmp(o) >= 0 }

func (r Rational) Float() float64 {
	return float64(r.Num) / float64(r.Den)
}

// Micros returns the nearest whole microsecond, half away from zero.
func (r Rational) Micros() int64 {
	return divRound(r.Num*1_000_000, r.Den)
}

// MicrosCeil returns the absolute value rounded up to whole microseconds.
// It is used for verifiable error bounds so a fractional tick cannot hide
// just above a limit.
func (r Rational) MicrosCeil() int64 {
	num := r.Num
	if num < 0 {
		num = -num
	}
	num *= 1_000_000
	q := num / r.Den
	if num%r.Den != 0 {
		q++
	}
	return q
}

func (r Rational) Abs() Rational {
	if r.Num < 0 {
		return r.Neg()
	}
	return r
}

func (r Rational) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%ds", r.Num)
	}
	return fmt.Sprintf("%d/%ds", r.Num, r.Den)
}

// CommonTimescale returns a timescale that can express both inputs exactly
// when the least common multiple fits in 32 bits. It falls back to the larger
// input otherwise.
func CommonTimescale(a, b uint32) uint32 {
	if a == 0 {
		return b
	}
	if b == 0 {
		return a
	}
	g := gcd(int64(a), int64(b))
	l := int64(a) / g * int64(b)
	if l <= 0 || l > math.MaxUint32 {
		if a > b {
			return a
		}
		return b
	}
	return uint32(l)
}

// Clamp returns v limited to [lo, hi].
func Clamp(v, lo, hi Rational) Rational {
	if v.Less(lo) {
		return lo
	}
	if v.Greater(hi) {
		return hi
	}
	return v
}
