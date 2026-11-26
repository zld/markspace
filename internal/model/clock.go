package model

import (
	"fmt"
	"math/big"
)

// Rational is an exact signed rational value. Media time bases use it to avoid
// choosing different rounding rules in cutting, silence and manifests.
type Rational struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

func (r Rational) Normalize() Rational {
	if r.Denominator == 0 {
		panic("model: zero rational denominator")
	}
	if r.Denominator < 0 {
		r.Numerator = -r.Numerator
		r.Denominator = -r.Denominator
	}
	if r.Numerator == 0 {
		return Rational{0, 1}
	}
	a := r.Numerator
	if a < 0 {
		a = -a
	}
	b := r.Denominator
	g := gcd(a, b)
	return Rational{Numerator: r.Numerator / g, Denominator: r.Denominator / g}
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func (r Rational) String() string {
	n := r.Normalize()
	if n.Denominator == 1 {
		return fmt.Sprintf("%d", n.Numerator)
	}
	return fmt.Sprintf("%d/%d", n.Numerator, n.Denominator)
}

func (r Rational) rat() *big.Rat {
	return big.NewRat(r.Numerator, r.Denominator)
}

// Cmp returns -1, 0 or 1. A zero denominator is invalid media metadata.
func (r Rational) Cmp(other Rational) int {
	return r.rat().Cmp(other.rat())
}

// Clock describes integer ticks where TickDuration is seconds per tick.
// For example, 48 kHz PCM has TickDuration: 1/48000.
type Clock struct {
	TickDuration Rational `json:"tick_duration"`
}

func NewClock(numerator, denominator int64) Clock {
	return Clock{TickDuration: Rational{Numerator: numerator, Denominator: denominator}.Normalize()}
}

// TicksFromSeconds converts an exact time in seconds to ticks using floor.
func (c Clock) TicksFromSeconds(seconds Rational) int64 {
	if c.TickDuration.Denominator == 0 {
		return 0
	}
	// seconds / tickDuration = seconds.N * c.D / (seconds.D * c.N)
	num := new(big.Int).Mul(big.NewInt(seconds.Numerator), big.NewInt(c.TickDuration.Denominator))
	den := new(big.Int).Mul(big.NewInt(seconds.Denominator), big.NewInt(c.TickDuration.Numerator))
	q, _ := new(big.Int).QuoRem(num, den, new(big.Int))
	return q.Int64()
}

// ConvertTicks maps integer ticks from one clock to another with ceiling.
// Ceiling is used for cut points so a requested point between samples moves to
// the first sample boundary at or after it.
func ConvertTicks(value int64, from, to Clock) int64 {
	if value == 0 {
		return 0
	}
	num := new(big.Int).Mul(big.NewInt(value), big.NewInt(from.TickDuration.Numerator))
	num.Mul(num, big.NewInt(to.TickDuration.Denominator))
	den := new(big.Int).Mul(big.NewInt(from.TickDuration.Denominator), big.NewInt(to.TickDuration.Numerator))
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 && value > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// Interval is a half-open [Start, End) range of integer ticks on Scale.
type Interval struct {
	Start int64    `json:"start"`
	End   int64    `json:"end"`
	Scale Rational `json:"scale"`
}

func (in Interval) Contains(tick int64) bool {
	return tick >= in.Start && tick < in.End
}

func (in Interval) DurationTicks() int64 {
	if in.End < in.Start {
		return 0
	}
	return in.End - in.Start
}
