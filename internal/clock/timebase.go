// Package clock defines the unified rational time base shared by every
// analysis, damage and publishing component.
//
// All media times are stored as exact fractions (numerator/denominator in
// seconds). No component may convert a media time to float before comparing
// it, so cross-track synchronization results are deterministic regardless of
// platform floating-point behaviour.
package clock

import (
	"errors"
	"fmt"
	"math"
)

// Rational is an exact time duration expressed in seconds as Num/Den.
// Den is always strictly positive after Normalize.
type Rational struct {
	Num int64
	Den int64
}

// ErrInvalidRational is returned when a rational value has no unique
// interpretation (zero denominator or an overflowing conversion).
var ErrInvalidRational = errors.New("clock: invalid rational time")

// New builds a normalized Rational from numerator and denominator.
func New(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrInvalidRational
	}
	if den < 0 {
		num, den = -num, -den
	}
	return normalize(Rational{Num: num, Den: den}), nil
}

// MustNew is New that panics on an invalid denominator.
// It is intended for compile-time-known constants in tests and fixtures.
func MustNew(num, den int64) Rational {
	r, err := New(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Zero is the rational representation of time 0.
func Zero() Rational { return Rational{Num: 0, Den: 1} }

func normalize(r Rational) Rational {
	if r.Num == 0 {
		return Rational{Num: 0, Den: 1}
	}
	g := gcd(abs64(r.Num), r.Den)
	r.Num /= g
	r.Den /= g
	return r
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Add returns r+o as an exact fraction.
func (r Rational) Add(o Rational) Rational {
	return normalize(Rational{
		Num: r.Num*o.Den + o.Num*r.Den,
		Den: r.Den * o.Den,
	})
}

// Sub returns r-o as an exact fraction.
func (r Rational) Sub(o Rational) Rational {
	return normalize(Rational{
		Num: r.Num*o.Den - o.Num*r.Den,
		Den: r.Den * o.Den,
	})
}

// Mul scales r by an integer factor (used for sample index times duration).
func (r Rational) Mul(n int64) Rational {
	return normalize(Rational{Num: r.Num * n, Den: r.Den})
}

// Cmp returns -1, 0 or 1 like bytes.Compare.
func (r Rational) Cmp(o Rational) int {
	l := r.Num * o.Den
	right := o.Num * r.Den
	switch {
	case l < right:
		return -1
	case l > right:
		return 1
	default:
		return 0
	}
}

// Abs returns the absolute duration.
func (r Rational) Abs() Rational {
	if r.Num < 0 {
		r.Num = -r.Num
	}
	return r
}

// IsZero reports whether the instant is exactly 0.
func (r Rational) IsZero() bool { return r.Num == 0 }

// Millis returns the duration rounded to whole milliseconds.
func (r Rational) Millis() int64 {
	return r.Scale(1000)
}

// Micros returns the duration rounded to whole microseconds.
func (r Rational) Micros() int64 {
	return r.Scale(1_000_000)
}

// Scale returns the duration expressed in unit 1/scale of a second,
// rounded to the nearest whole unit (half away from zero).
func (r Rational) Scale(scale int64) int64 {
	if r.Den == 0 {
		return 0
	}
	num := r.Num * scale
	q := num / r.Den
	rem := num % r.Den
	if rem != 0 {
		if (num > 0) == (rem > 0) && abs64(rem)*2 >= r.Den {
			q++
		} else if (num > 0) != (rem > 0) && abs64(rem)*2 > r.Den {
			q--
		}
	}
	return q
}

// Float is only for human-readable output; comparisons must stay rational.
func (r Rational) Float() float64 {
	if r.Den == 0 {
		return math.NaN()
	}
	return float64(r.Num) / float64(r.Den)
}

// String renders the exact fraction plus its millisecond value.
func (r Rational) String() string {
	if r.Den == 0 {
		return "<invalid-time>"
	}
	return fmt.Sprintf("%d/%ds(%.3fms)", r.Num, r.Den, float64(r.Millis()))
}

// FromTicks converts a clock tick count (e.g. an RTP timestamp difference)
// at the given clock rate in Hz into an exact Rational duration.
func FromTicks(ticks int64, clockRate int64) (Rational, error) {
	if clockRate <= 0 {
		return Rational{}, fmt.Errorf("%w: clock rate %d", ErrInvalidRational, clockRate)
	}
	return New(ticks, clockRate)
}

// ToTicks is the inverse of FromTicks: it converts an instant onto a clock
// and reports the exact tick count plus the rounding remainder.
func (r Rational) ToTicks(clockRate int64) (ticks int64, remainder Rational, err error) {
	if clockRate <= 0 {
		return 0, Rational{}, fmt.Errorf("%w: clock rate %d", ErrInvalidRational, clockRate)
	}
	scaled, err := New(r.Num*clockRate, r.Den)
	if err != nil {
		return 0, Rational{}, err
	}
	ticks = scaled.Num / scaled.Den
	rem := scaled.Num % scaled.Den
	remainder = normalize(Rational{Num: rem, Den: scaled.Den})
	return ticks, remainder, nil
}
