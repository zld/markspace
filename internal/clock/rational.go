// Package clock provides rational-number media time so that all tracks share
// one exact time base. Floating-point accumulation is forbidden: every media
// timestamp is kept as an exact rational (num/den) and all arithmetic is
// overflow-checked.
package clock

import (
	"errors"
	"fmt"
	"math/big"
)

// ErrOverflow is returned when a rational operation would overflow int64.
var ErrOverflow = errors.New("clock: rational arithmetic overflow")

// ErrInvalidDenominator is returned for a zero or negative denominator.
var ErrInvalidDenominator = errors.New("clock: denominator must be positive")

// Rational is an exact, normalized fraction Num/Den with Den > 0.
type Rational struct {
	Num int64
	Den int64
}

// New builds a normalized Rational. The denominator must be positive.
func New(num, den int64) (Rational, error) {
	if den <= 0 {
		return Rational{}, ErrInvalidDenominator
	}
	g := gcd(abs(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

// Must is New that panics on error; for constants in tests and fixtures.
func Must(num, den int64) Rational {
	r, err := New(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Zero is the additive identity.
func Zero() Rational { return Rational{Num: 0, Den: 1} }

// FromTicks converts media clock ticks at the given timescale to seconds.
func FromTicks(ticks int64, timescale uint32) (Rational, error) {
	if timescale == 0 {
		return Rational{}, ErrInvalidDenominator
	}
	return New(ticks, int64(timescale))
}

// Ticks converts the rational back to ticks at the given timescale,
// rounding toward zero. The residual (sub-tick part) is dropped.
func (r Rational) Ticks(timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, ErrInvalidDenominator
	}
	n := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(int64(timescale)))
	n.Quo(n, big.NewInt(r.Den))
	if !n.IsInt64() {
		return 0, ErrOverflow
	}
	return n.Int64(), nil
}

// Add returns r+o, checked for overflow.
func (r Rational) Add(o Rational) (Rational, error) {
	return binop(r, o, func(a, b *big.Int) *big.Int { return a.Add(a, b) })
}

// Sub returns r-o, checked for overflow.
func (r Rational) Sub(o Rational) (Rational, error) {
	return binop(r, o, func(a, b *big.Int) *big.Int { return a.Sub(a, b) })
}

// Mul returns r*o, checked for overflow.
func (r Rational) Mul(o Rational) (Rational, error) {
	n := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Num))
	d := new(big.Int).Mul(big.NewInt(r.Den), big.NewInt(o.Den))
	return fromBig(n, d)
}

// Abs returns |r|.
func (r Rational) Abs() Rational {
	if r.Num < 0 {
		return Rational{Num: -r.Num, Den: r.Den}
	}
	return r
}

// Cmp compares r to o: -1, 0 or +1. Exact, never overflows (big.Int cross-mult).
func (r Rational) Cmp(o Rational) int {
	a := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
	b := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
	return a.Cmp(b)
}

// IsNegative reports whether r < 0.
func (r Rational) IsNegative() bool { return r.Num < 0 }

// ToMillis renders r (seconds) as milliseconds for display only. This is the
// sole float conversion in the pipeline and is never fed back into math.
func (r Rational) ToMillis() float64 {
	return float64(r.Num) * 1000.0 / float64(r.Den)
}

// String renders the rational as num/den.
func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

func binop(r, o Rational, f func(a, b *big.Int) *big.Int) (Rational, error) {
	a := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
	b := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
	n := f(a, b)
	d := new(big.Int).Mul(big.NewInt(r.Den), big.NewInt(o.Den))
	return fromBig(n, d)
}

func fromBig(n, d *big.Int) (Rational, error) {
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(n), d)
	n.Quo(n, g)
	d.Quo(d, g)
	if !n.IsInt64() || !d.IsInt64() {
		return Rational{}, ErrOverflow
	}
	if d.Sign() < 0 {
		n.Neg(n)
		d.Neg(d)
	}
	return Rational{Num: n.Int64(), Den: d.Int64()}, nil
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

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
