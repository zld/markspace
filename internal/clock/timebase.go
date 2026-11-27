// Package clock provides an exact rational time base shared by scanning,
// transport and merge subsystems.
//
// Timestamps are never stored as floating point values.  A Rational keeps the
// original tick so callers can always reconstruct the source representation,
// and conversion between time bases uses integer arithmetic so there is no
// silent drift.
package clock

import (
	"errors"
	"fmt"
)

// Rational is a tick value expressed in Num/Den seconds per tick.
// Den is always positive; a zero Rational has Num == 0.
type Rational struct {
	Num int64 // numerator (seconds)
	Den int64 // denominator (ticks), always > 0
}

// NewRational builds a rational, normalising the sign and reducing it.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), den)
	if g > 1 {
		num /= g
		den /= g
	}
	return Rational{Num: num, Den: den}, nil
}

// MustRational panics when NewRational would return an error.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Add returns r+o in a reduced rational.
func (r Rational) Add(o Rational) Rational {
	num := r.Num*o.Den + o.Num*r.Den
	den := r.Den * o.Den
	out, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return out
}

// Cmp returns -1, 0 or 1 when r is less than, equal to or greater than o.
func (r Rational) Cmp(o Rational) int {
	lhs := r.Num * o.Den
	rhs := o.Num * r.Den
	switch {
	case lhs < rhs:
		return -1
	case lhs > rhs:
		return 1
	default:
		return 0
	}
}

// String renders the rational as "num/den".
func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// TimeBase describes how source ticks map to seconds: seconds = Tick * Num / Den.
// Tick is the raw value kept from the source so no information is lost.
type TimeBase struct {
	Tick int64
	Base Rational
}

// NewTimeBase validates the rational base and attaches the raw tick.
func NewTimeBase(tick int64, num, den int64) (TimeBase, error) {
	base, err := NewRational(num, den)
	if err != nil {
		return TimeBase{}, err
	}
	return TimeBase{Tick: tick, Base: base}, nil
}

// ToSeconds converts the raw tick to an exact rational number of seconds.
func (t TimeBase) ToSeconds() Rational {
	out, err := NewRational(t.Tick*t.Base.Num, t.Base.Den)
	if err != nil {
		panic(err)
	}
	return out
}

// Convert re-expresses the timestamp in another rational time base.
//
// It returns an error when the destination tick would be fractional: in that
// case there is no unique mapping and callers must report a time-base conflict
// instead of rounding or silently shifting the value.
func (t TimeBase) Convert(dst Rational) (int64, error) {
	if dst.Num == 0 {
		return 0, errors.New("clock: zero numerator destination base")
	}
	num := t.Tick * t.Base.Num * dst.Den
	den := t.Base.Den * dst.Num
	if num%den != 0 {
		return 0, fmt.Errorf("clock: non-unique conversion %d ticks in %s to %s",
			t.Tick, t.Base, dst)
	}
	return num / den, nil
}

// CommonBase returns the reduced sum-base used to compare two rationals in
// seconds without losing precision.
func CommonBase(a, b Rational) Rational {
	out, err := NewRational(1, lcm(a.Den, b.Den))
	if err != nil {
		panic(err)
	}
	return out
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func lcm(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	return a / gcd(a, b) * b
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
