// Package clock defines the integer-only media time model shared by every
// subsystem. Media time is always represented as whole ticks on an explicit
// timescale; conversions go through exact rational arithmetic so rounding can
// never accumulate drift across repeated conversions.
package clock

import (
	"fmt"
	"math/big"
)

// Rational is an exact signed rational number. The denominator is always
// strictly positive; the zero value is 0/1.
type Rational struct {
	num int64
	den int64 // always > 0
}

// NewRational builds a rational and normalizes its sign and gcd.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, fmt.Errorf("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rational{num: 0, den: 1}, nil
	}
	g := gcd(abs64(num), den)
	return Rational{num: num / g, den: den / g}, nil
}

// Num returns the (normalized) numerator.
func (r Rational) Num() int64 { return r.num }

// Den returns the (normalized, positive) denominator.
func (r Rational) Den() int64 {
	if r.den == 0 {
		return 1
	}
	return r.den
}

func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.Num(), r.Den()) }

// Mul scales a tick count by an exact rational and rounds to nearest with
// ties away from zero. It is the single conversion primitive: each call is
// independently exact, so chained conversions never accumulate drift.
func Mul(ticks int64, scale Rational) int64 {
	if ticks == 0 || scale.num == 0 {
		return 0
	}
	num := big.NewInt(ticks)
	num.Mul(num, big.NewInt(scale.Num()))
	den := big.NewInt(scale.Den())
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(num, den, rem)
	// Round half away from zero: compare 2*|rem| with den.
	twice := new(big.Int).Abs(rem)
	twice.Mul(twice, big.NewInt(2))
	if twice.Cmp(den) >= 0 {
		if num.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	return q.Int64()
}

// Scale returns the exact Rational converting ticks at srcHz to ticks at
// dstHz, i.e. multiply source ticks by Scale(src,dst) to get destination
// ticks. Both arguments must be positive.
func Scale(srcHz, dstHz uint32) (Rational, error) {
	if srcHz == 0 || dstHz == 0 {
		return Rational{}, fmt.Errorf("clock: timescale must be positive (src=%d dst=%d)", srcHz, dstHz)
	}
	return NewRational(int64(dstHz), int64(srcHz))
}

// Convert re-bases ticks from srcHz onto dstHz using exact rational
// arithmetic (nearest, ties away from zero).
func Convert(ticks int64, srcHz, dstHz uint32) (int64, error) {
	s, err := Scale(srcHz, dstHz)
	if err != nil {
		return 0, err
	}
	return Mul(ticks, s), nil
}

// WrapCount treats v as a value on a cyclic counter with the given modulo
// (2^bits) and returns a monotonically increasing unwrapped value given the
// previous unwrapped value. bits must be between 1 and 64.
func WrapCount(prev uint64, v uint64, bits uint) uint64 {
	mod := uint64(1) << bits
	base := prev - (prev % mod)
	cand := base + v
	half := mod / 2
	diff := diffMod(cand, prev, mod)
	if diff > half {
		cand -= mod
	} else if diff == half && cand < prev {
		cand += mod
	}
	if cand+mod/2 < prev {
		cand += mod
	}
	if cand > prev+mod/2 {
		cand -= mod
	}
	return cand
}

func diffMod(a, b, mod uint64) uint64 {
	if a >= b {
		return a - b
	}
	return mod - (b - a)
}

// Wrap32 unwraps a 32-bit RTP timestamp/sequence relative to a prior
// unwrapped value.
func Wrap32(prev uint64, v uint32) uint64 { return WrapCount(prev, uint64(v), 32) }

// Wrap16 unwraps a 16-bit RTP sequence number relative to a prior unwrapped
// value.
func Wrap16(prev uint64, v uint16) uint64 { return WrapCount(prev, uint64(v), 16) }

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
