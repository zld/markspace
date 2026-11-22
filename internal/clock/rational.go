// Package clock provides an exact rational time base shared by every
// subsystem, plus SMPTE timecode parsing and wrap expansion.
package clock

import "fmt"

// Rational is an exact non-negative-or-negative fraction of a second.
// Den is always positive and the fraction is kept reduced.
type Rational struct {
	Num int64
	Den int64
}

// NewRat builds a reduced rational. A zero denominator falls back to 1 so
// that malformed input can never panic a pipeline.
func NewRat(num, den int64) Rational {
	if den == 0 {
		den = 1
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs(num), den)
	if g > 1 {
		num /= g
		den /= g
	}
	return Rational{Num: num, Den: den}
}

// Zero is the origin of any timeline.
var Zero = Rational{Num: 0, Den: 1}

// FromMillis converts integer milliseconds to a rational second.
func FromMillis(ms int64) Rational { return NewRat(ms, 1000) }

// FromMicros converts integer microseconds to a rational second.
func FromMicros(us int64) Rational { return NewRat(us, 1000000) }

// FromTicks converts a media clock tick count and timescale to seconds.
func FromTicks(ticks, timescale int64) Rational { return NewRat(ticks, timescale) }

// Add returns r+o.
func (r Rational) Add(o Rational) Rational {
	if r.Den == o.Den {
		return NewRat(r.Num+o.Num, r.Den)
	}
	return NewRat(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den)
}

// Sub returns r-o.
func (r Rational) Sub(o Rational) Rational {
	if r.Den == o.Den {
		return NewRat(r.Num-o.Num, r.Den)
	}
	return NewRat(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den)
}

// MulInt scales r by an integer.
func (r Rational) MulInt(n int64) Rational { return NewRat(r.Num*n, r.Den) }

// Scale multiplies r by num/den.
func (r Rational) Scale(num, den int64) Rational {
	if den == 0 {
		den = 1
	}
	return NewRat(r.Num*num, r.Den*den)
}

// Cmp returns -1, 0 or 1 for r<o, r==o, r>o.
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

// Equal reports exact rational equality.
func (r Rational) Equal(o Rational) bool { return r.Cmp(o) == 0 }

// IsZero reports whether r is exactly zero.
func (r Rational) IsZero() bool { return r.Num == 0 }

// Float returns the fraction as a float64 second.
func (r Rational) Float() float64 { return float64(r.Num) / float64(r.Den) }

// Millis returns the fraction in float64 milliseconds.
func (r Rational) Millis() float64 { return r.Float() * 1000 }

// Micros returns the fraction in float64 microseconds.
func (r Rational) Micros() float64 { return r.Float() * 1e6 }

// Ticks rounds r to an integer count on a clock with the given timescale.
func (r Rational) Ticks(timescale int64) int64 {
	return roundDiv(r.Num*timescale, r.Den)
}

// String renders an exact and stable representation; it is used by
// manifest digests so float formatting can never change.
func (r Rational) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%d", r.Num)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func roundDiv(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}
