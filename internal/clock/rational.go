// Package clock implements exact integer-rational media time bases.
//
// All conversions between track timescales and the unified timeline tick
// scale are performed with big.Int/big.Rat arithmetic. Floating point is
// never used, so repeated conversions cannot accumulate rounding error.
package clock

import (
	"fmt"
	"math/big"
)

// TickRate is the unified timeline timescale: one tick is 1/TickRate second.
// 1e9 keeps nanosecond granularity for every common MP4 timescale and for
// the 90kHz RTP video clock without loss (both divide 1e9 evenly).
const TickRate int64 = 1_000_000_000

// Rational is a non-negative exact fraction Num/Den. Den is always > 0 and
// the representation is kept in reduced form so equality is structural.
type Rational struct {
	num *big.Int
	den *big.Int
}

// NewRational builds a reduced rational. It panics on den <= 0.
func NewRational(num, den int64) Rational {
	if den <= 0 {
		panic(fmt.Sprintf("clock: non-positive denominator %d", den))
	}
	n := big.NewInt(num)
	d := big.NewInt(den)
	g := new(big.Int).GCD(nil, nil, n, d)
	n.Quo(n, g)
	d.Quo(d, g)
	return Rational{num: n, den: d}
}

// Num returns the numerator.
func (r Rational) Num() *big.Int { return new(big.Int).Set(r.num) }

// Den returns the denominator (always positive).
func (r Rational) Den() *big.Int { return new(big.Int).Set(r.den) }

// IsZero reports whether the value is zero.
func (r Rational) IsZero() bool { return r.num.Sign() == 0 }

// String renders "num/den" (or an integer when den == 1).
func (r Rational) String() string {
	if r.den.Cmp(big.NewInt(1)) == 0 {
		return r.num.String()
	}
	return r.num.String() + "/" + r.den.String()
}

// Cmp returns -1, 0, +1 comparing r with o exactly.
func (r Rational) Cmp(o Rational) int {
	a := new(big.Int).Mul(r.num, o.den)
	b := new(big.Int).Mul(o.num, r.den)
	return a.Cmp(b)
}

// Equal reports exact equality.
func (r Rational) Equal(o Rational) bool { return r.Cmp(o) == 0 }

// Mul returns r*o in reduced form.
func (r Rational) Mul(o Rational) Rational {
	return normalize(new(big.Int).Mul(r.num, o.num), new(big.Int).Mul(r.den, o.den))
}

// Add returns r+o in reduced form.
func (r Rational) Add(o Rational) Rational {
	n := new(big.Int).Add(
		new(big.Int).Mul(r.num, o.den),
		new(big.Int).Mul(o.num, r.den),
	)
	d := new(big.Int).Mul(r.den, o.den)
	return normalize(n, d)
}

// Sub returns r-o. It panics when the result would be negative: media
// timelines in this library are non-negative, and a negative result usually
// means an unwrap or alignment bug upstream.
func (r Rational) Sub(o Rational) Rational {
	n := new(big.Int).Sub(
		new(big.Int).Mul(r.num, o.den),
		new(big.Int).Mul(o.num, r.den),
	)
	if n.Sign() < 0 {
		panic("clock: negative rational subtraction")
	}
	return normalize(n, new(big.Int).Mul(r.den, o.den))
}

// Floor returns the greatest integer <= r.
func (r Rational) Floor() *big.Int {
	return new(big.Int).Quo(r.num, r.den)
}

// Round returns the nearest integer, halves away from zero.
func (r Rational) Round() *big.Int {
	q := new(big.Int)
	m := new(big.Int)
	q.QuoRem(r.num, r.den, m)
	twoM := new(big.Int).Lsh(m, 1)
	if twoM.Cmp(r.den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func normalize(n, d *big.Int) Rational {
	if d.Sign() <= 0 {
		panic("clock: non-positive denominator")
	}
	g := new(big.Int).GCD(nil, nil, n, d)
	n.Quo(n, g)
	d.Quo(d, g)
	return Rational{num: n, den: d}
}

// ToTicks converts a value expressed in sourceTimeScale units (per second)
// into unified ticks at TickRate, rounding half away from zero.
//
//	ticks = round(value * TickRate / sourceTimeScale)
func ToTicks(value int64, sourceTimeScale uint32) int64 {
	if sourceTimeScale == 0 {
		panic("clock: zero source timescale")
	}
	num := new(big.Int).Mul(big.NewInt(value), big.NewInt(TickRate))
	q := new(big.Int).Quo(num, big.NewInt(int64(sourceTimeScale)))
	rem := new(big.Int).Mod(num, big.NewInt(int64(sourceTimeScale)))
	if new(big.Int).Lsh(rem, 1).Cmp(big.NewInt(int64(sourceTimeScale))) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// FromTicks converts unified ticks back into sourceTimeScale units, rounding
// half away from zero. It is the exact inverse pair of ToTicks for values
// whose round trip stays integral; callers check equality explicitly.
func FromTicks(ticks int64, sourceTimeScale uint32) int64 {
	if sourceTimeScale == 0 {
		panic("clock: zero source timescale")
	}
	num := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(int64(sourceTimeScale)))
	q := new(big.Int).Quo(num, big.NewInt(TickRate))
	rem := new(big.Int).Mod(num, big.NewInt(TickRate))
	if new(big.Int).Lsh(rem, 1).Cmp(big.NewInt(TickRate)) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// Rate converts a drift slope expressed as (slopeNum/slopeDen) into a
// Rational for the timeline repair rules.
func Rate(slopeNum, slopeDen int64) Rational {
	return NewRational(slopeNum, slopeDen)
}
