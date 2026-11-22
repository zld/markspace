// Package model defines the shared media time base and the pipeline
// error model. All subsystems convert track-local timestamps into one
// unified rational time base; floating point accumulation is forbidden.
package model

import (
	"fmt"
	"math/big"
)

// UnifiedScale is the pipeline-wide time base in ticks per second.
const UnifiedScale int64 = 90000

// Hard input limits. Inputs exceeding them are rejected before processing.
const (
	MaxTracks     = 64
	MaxFrameDim   = 4096
	MaxInputBytes = int64(2) << 30 // 2 GiB
)

// Rational is an exact point in time or duration: Num/Den seconds.
// The value is always kept normalized (Den > 0, gcd(|Num|, Den) == 1).
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NewRational builds a normalized Rational. Den must be positive.
func NewRational(num, den int64) (Rational, error) {
	if den <= 0 {
		return Rational{}, fmt.Errorf("rational: non-positive denominator %d", den)
	}
	g := gcd64(num, den)
	if g < 0 {
		g = -g
	}
	if g == 0 {
		g = 1
	}
	return Rational{Num: num / g, Den: den / g}, nil
}

// FromTicks converts value ticks at the given scale (ticks/second) into
// an exact Rational number of seconds.
func FromTicks(value, scale int64) (Rational, error) {
	if scale <= 0 {
		return Rational{}, fmt.Errorf("rational: invalid timescale %d", scale)
	}
	return NewRational(value, scale)
}

func gcd64(a, b int64) int64 {
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

func (r Rational) big() *big.Rat {
	return big.NewRat(r.Num, r.Den)
}

// Add returns r+o. It fails if the exact result overflows int64.
func (r Rational) Add(o Rational) (Rational, error) {
	res := new(big.Rat).Add(r.big(), o.big())
	return fromBig(res, "add")
}

// Sub returns r-o. It fails if the exact result overflows int64.
func (r Rational) Sub(o Rational) (Rational, error) {
	res := new(big.Rat).Sub(r.big(), o.big())
	return fromBig(res, "sub")
}

// Cmp compares two rationals exactly: -1, 0 or +1.
func (r Rational) Cmp(o Rational) int {
	return r.big().Cmp(o.big())
}

// IsZero reports whether r == 0.
func (r Rational) IsZero() bool { return r.Num == 0 }

// ToTicks converts r to an integer number of ticks at scale. The
// conversion must be exact; any remainder or int64 overflow is an
// error so that a failing track timestamp can be located precisely.
func (r Rational) ToTicks(scale int64) (int64, error) {
	if scale <= 0 {
		return 0, fmt.Errorf("rational: invalid target scale %d", scale)
	}
	num := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(scale))
	den := big.NewInt(r.Den)
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 {
		return 0, fmt.Errorf("rational: %d/%d s is not integral at scale %d", r.Num, r.Den, scale)
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("rational: tick value overflows int64 at scale %d", scale)
	}
	return q.Int64(), nil
}

func fromBig(v *big.Rat, op string) (Rational, error) {
	if !v.Num().IsInt64() || !v.Denom().IsInt64() {
		return Rational{}, fmt.Errorf("rational: %s overflows int64", op)
	}
	return Rational{Num: v.Num().Int64(), Den: v.Denom().Int64()}, nil
}

func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// Interval is a half-open time range [Start, Start+Dur) on the
// unified time base.
type Interval struct {
	Start Rational `json:"start"`
	Dur   Rational `json:"dur"`
}

// End returns the exclusive end of the interval.
func (iv Interval) End() (Rational, error) { return iv.Start.Add(iv.Dur) }

// Contains reports whether t falls inside the interval.
func (iv Interval) Contains(t Rational) (bool, error) {
	end, err := iv.End()
	if err != nil {
		return false, err
	}
	return iv.Start.Cmp(t) <= 0 && t.Cmp(end) < 0, nil
}
