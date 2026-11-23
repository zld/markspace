// Package model defines the shared time base and structured error model used by
// every stage of the media pipeline. All time arithmetic is integer rational
// arithmetic; floating point must never be used to accumulate media time.
package model

import (
	"errors"
	"math/big"
)

// Rat is an exact signed rational number: Num/Den, Den always positive.
// The zero value is not a valid Rat (Den == 0); use NewRat.
type Rat struct {
	Num int64
	Den int64 // always > 0
}

// NewRat constructs a normalized rational. It returns an error on a zero
// denominator or on overflow while normalizing.
func NewRat(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, errors.New("model: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g, ok := i64GCD(abs64(num), den)
	if !ok {
		return Rat{}, errors.New("model: gcd overflow")
	}
	num /= g
	den /= g
	return Rat{Num: num, Den: den}, nil
}

// MustRat panics if the rational cannot be constructed. Test/helper use only.
func MustRat(num, den int64) Rat {
	r, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Zero returns the canonical zero rational 0/1.
func Zero() Rat { return Rat{Num: 0, Den: 1} }

// Cmp returns -1, 0, +1 like bytes.Compare. Products are checked for overflow.
func (r Rat) Cmp(o Rat) int {
	l, err1 := mul64(r.Num, o.Den)
	rr, err2 := mul64(o.Num, r.Den)
	if err1 != nil || err2 != nil {
		// Fall back to arbitrary precision only for the comparison itself.
		return big.NewRat(r.Num, r.Den).Cmp(big.NewRat(o.Num, o.Den))
	}
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// Add returns r+o with overflow-checked products.
func (r Rat) Add(o Rat) (Rat, error) {
	if r.Den == 0 {
		return o, nil
	}
	if o.Den == 0 {
		return r, nil
	}
	ln, e1 := mul64(r.Num, o.Den)
	rn, e2 := mul64(o.Num, r.Den)
	d, e3 := mul64(r.Den, o.Den)
	if e1 != nil || e2 != nil || e3 != nil {
		return Rat{}, errors.New("model: rational add overflow")
	}
	n, err := add64(ln, rn)
	if err != nil {
		return Rat{}, err
	}
	return NewRat(n, d)
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) (Rat, error) {
	return r.Add(Rat{Num: -o.Num, Den: o.Den})
}

// Mul returns r*o.
func (r Rat) Mul(o Rat) (Rat, error) {
	n, e1 := mul64(r.Num, o.Num)
	d, e2 := mul64(r.Den, o.Den)
	if e1 != nil || e2 != nil {
		return Rat{}, errors.New("model: rational mul overflow")
	}
	return NewRat(n, d)
}

// ScaleInt multiplies r by an integer tick count and rounds half away from
// zero to an int64, rejecting overflow.
func (r Rat) ScaleInt(ticks int64) (int64, error) {
	p, err := mul64(r.Num, ticks)
	if err != nil {
		return 0, err
	}
	return divRound(p, r.Den), nil
}

// Floor returns the greatest integer <= r.
func (r Rat) Floor() int64 {
	q, rem := r.Num/r.Den, r.Num%r.Den
	if rem < 0 {
		q--
	}
	return q
}

// Round returns the nearest integer, halves rounded away from zero.
func (r Rat) Round() int64 {
	return divRound(r.Num, r.Den)
}

// IsZero reports whether the rational equals zero.
func (r Rat) IsZero() bool { return r.Den != 0 && r.Num == 0 }

// divRound divides n/d rounding half away from zero; d must be positive.
// Uses truncating division so the sign is applied before division.
func divRound(n, d int64) int64 {
	switch {
	case n > 0:
		return (n + d/2) / d
	case n < 0:
		return -((-n + d/2) / d)
	default:
		return 0
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		if v == -1<<63 {
			return 1<<63 - 1 // saturating abs, GCD result stays correct for inputs here
		}
		return -v
	}
	return v
}

func i64GCD(a, b int64) (int64, bool) {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1, true
	}
	return a, true
}

// mul64 returns a*b or an error when the int64 result overflows.
func mul64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	v := a * b
	// A signed multiply is exact iff dividing the result back reproduces b.
	if a == -1<<63 {
		// minInt64 only fits when the other operand is 0 or 1.
		if b != 1 {
			return 0, errors.New("model: integer multiply overflow")
		}
		return v, nil
	}
	if v/a != b {
		return 0, errors.New("model: integer multiply overflow")
	}
	return v, nil
}

// add64 returns a+b or an error on int64 overflow.
func add64(a, b int64) (int64, error) {
	v := a + b
	if (a > 0 && b > 0 && v < 0) || (a < 0 && b < 0 && v >= 0) {
		return 0, errors.New("model: integer add overflow")
	}
	return v, nil
}
