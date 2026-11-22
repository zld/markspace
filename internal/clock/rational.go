// Package clock provides exact rational media time and repair of damaged
// track clocks (wraparound, backward steps, late packets and origin offsets).
//
// All arithmetic is integer based. Floating point is never used to accumulate
// time; Float64 exists only for optional display and is never fed back into
// the pipeline.
package clock

import (
	"errors"
	"fmt"
)

// ErrOverflow reports that an exact rational operation would not fit in int64.
var ErrOverflow = errors.New("rational overflow")

// ErrZeroDenominator reports an invalid rational construction.
var ErrZeroDenominator = errors.New("rational denominator must not be zero")

// Rational is an exact media timestamp: num/den seconds, always kept in
// lowest terms with a positive denominator. The zero value represents 0/1.
type Rational struct {
	num int64
	den int64
}

// Zero is the additive identity.
var Zero = Rational{num: 0, den: 1}

// One is one second.
var One = Rational{num: 1, den: 1}

// New builds a normalized rational. It returns ErrZeroDenominator when den is
// zero and ErrOverflow when the value cannot be represented.
func New(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrZeroDenominator
	}
	if num == 0 {
		return Zero, nil
	}
	if den < 0 {
		var err error
		if num, err = negate(num); err != nil {
			return Rational{}, err
		}
		den = -den
	}
	g := gcd(abs64(num), uint64(den))
	return Rational{num: num / int64(g), den: den / int64(g)}, nil
}

// FromInt converts a whole number of seconds.
func FromInt(n int64) Rational { return Rational{num: n, den: 1} }

// FromTicks converts an integer tick count at the given timescale into exact
// seconds. A zero timescale is rejected.
func FromTicks(ticks int64, timescale uint32) (Rational, error) {
	if timescale == 0 {
		return Rational{}, fmt.Errorf("timescale must not be zero")
	}
	return New(ticks, int64(timescale))
}

// Num returns the signed numerator.
func (r Rational) Num() int64 { return r.canon().num }

// Den returns the positive denominator.
func (r Rational) Den() int64 { return r.canon().den }

func (r Rational) canon() Rational {
	if r.den == 0 {
		return Zero
	}
	if r.den < 0 {
		// Only reachable through zero-value structs crafted outside the package;
		// normalize defensively without worrying about MinInt64 here because
		// constructors never produce a negative denominator.
		return Rational{num: -r.num, den: -r.den}
	}
	return r
}

// IsZero reports whether r == 0.
func (r Rational) IsZero() bool { r = r.canon(); return r.num == 0 }

// Sign returns -1, 0 or +1.
func (r Rational) Sign() int {
	r = r.canon()
	switch {
	case r.num < 0:
		return -1
	case r.num > 0:
		return 1
	default:
		return 0
	}
}

// Neg returns -r.
func (r Rational) Neg() (Rational, error) {
	r = r.canon()
	n, err := negate(r.num)
	if err != nil {
		return Rational{}, err
	}
	return Rational{num: n, den: r.den}, nil
}

// Add returns r + o exactly.
func (r Rational) Add(o Rational) (Rational, error) {
	r, o = r.canon(), o.canon()
	if r.den == o.den {
		n, err := addInt64(r.num, o.num)
		if err != nil {
			return Rational{}, err
		}
		return New(n, r.den)
	}
	a, err := mulInt64(r.num, o.den)
	if err != nil {
		return Rational{}, err
	}
	b, err := mulInt64(o.num, r.den)
	if err != nil {
		return Rational{}, err
	}
	n, err := addInt64(a, b)
	if err != nil {
		return Rational{}, err
	}
	d, err := mulInt64(r.den, o.den)
	if err != nil {
		return Rational{}, err
	}
	return New(n, d)
}

// Sub returns r - o exactly.
func (r Rational) Sub(o Rational) (Rational, error) {
	neg, err := o.Neg()
	if err != nil {
		return Rational{}, err
	}
	return r.Add(neg)
}

// MulInt returns r * k exactly.
func (r Rational) MulInt(k int64) (Rational, error) {
	r = r.canon()
	n, err := mulInt64(r.num, k)
	if err != nil {
		return Rational{}, err
	}
	return New(n, r.den)
}

// DivInt returns r / k exactly.
func (r Rational) DivInt(k int64) (Rational, error) {
	r = r.canon()
	if k == 0 {
		return Rational{}, ErrZeroDenominator
	}
	d, err := mulInt64(r.den, k)
	if err != nil {
		return Rational{}, err
	}
	return New(r.num, d)
}

// Cmp compares r and o.
func (r Rational) Cmp(o Rational) int {
	r, o = r.canon(), o.canon()
	rs, os := r.num < 0, o.num < 0
	if rs != os {
		if rs {
			return -1
		}
		return 1
	}
	c := cmp128(mul128(abs64(r.num), uint64(o.den)), mul128(abs64(o.num), uint64(r.den)))
	if rs {
		return -c
	}
	return c
}

// Less reports r < o.
func (r Rational) Less(o Rational) bool { return r.Cmp(o) < 0 }

// Equal reports r == o.
func (r Rational) Equal(o Rational) bool { return r.Cmp(o) == 0 }

// FloorTicks converts r to ticks at timescale, rounding toward -infinity. The
// result is exact up to the division remainder, i.e. within one tick of r.
func (r Rational) FloorTicks(timescale uint32) (int64, error) {
	r = r.canon()
	if timescale == 0 {
		return 0, fmt.Errorf("timescale must not be zero")
	}
	num, err := mulInt64(r.num, int64(timescale))
	if err != nil {
		return 0, err
	}
	q := num / r.den
	if num%r.den != 0 && num < 0 {
		q--
	}
	return q, nil
}

// Float64 is a display-only approximation. It must never be used to
// accumulate or compare media time inside the pipeline.
func (r Rational) Float64() float64 {
	r = r.canon()
	return float64(r.num) / float64(r.den)
}

// String renders the exact value; whole numbers omit the denominator.
func (r Rational) String() string {
	r = r.canon()
	if r.den == 1 {
		return fmt.Sprintf("%d", r.num)
	}
	return fmt.Sprintf("%d/%d", r.num, r.den)
}
