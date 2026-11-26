// Package clock contains exact rational time bases and timestamp repair.
package clock

import (
	"encoding/json"
	"fmt"
	"math/big"
)

// Rational is an immutable signed rational number.  Den is always positive.
// All cumulative time arithmetic uses Rational; float64 is used only to format
// an already-rounded percentage for presentation.
type Rational struct {
	N int64 `json:"n"`
	D int64 `json:"d"`
}

// NewRational reduces n/d and normalizes the sign onto N.
func NewRational(n, d int64) Rational {
	if d == 0 {
		panic("clock: zero rational denominator")
	}
	if d < 0 {
		n, d = -n, -d
	}
	if n == 0 {
		return Rational{N: 0, D: 1}
	}
	g := new(big.Int).GCD(nil, nil, big.NewInt(abs64(n)), big.NewInt(d))
	g64 := g.Int64()
	return Rational{N: n / g64, D: d / g64}
}

// TickTime returns t ticks on a timescale measured in ticks per second.
func TickTime(ticks int64, timescale uint32) Rational {
	if timescale == 0 {
		panic("clock: zero timescale")
	}
	return NewRational(ticks, int64(timescale))
}

// TimeBase describes an integer number of ticks per second.
type TimeBase struct {
	TicksPerSecond uint32 `json:"timescale"`
}

// Time converts a tick count to exact seconds.
func (tb TimeBase) Time(ticks int64) Rational {
	return TickTime(ticks, tb.TicksPerSecond)
}

// Tick returns the duration of one tick as rational seconds.
func (tb TimeBase) Tick() Rational {
	return Rational{N: 1, D: int64(tb.TicksPerSecond)}
}

// Ticks converts an exact rational duration to this time base.  The boolean
// result is false when the duration is not an integral number of ticks.
func (tb TimeBase) Ticks(v Rational) (int64, bool) {
	if tb.TicksPerSecond == 0 {
		return 0, false
	}
	num := big.NewInt(v.N)
	num.Mul(num, big.NewInt(int64(tb.TicksPerSecond)))
	den := big.NewInt(v.D)
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(num, den, r)
	if r.Sign() != 0 || !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}

// Add returns a+b without floating-point accumulation.
func (a Rational) Add(b Rational) Rational {
	an, ad := big.NewInt(a.N), big.NewInt(a.D)
	bn, bd := big.NewInt(b.N), big.NewInt(b.D)
	num := new(big.Int).Mul(an, bd)
	num.Add(num, new(big.Int).Mul(bn, ad))
	den := new(big.Int).Mul(ad, bd)
	return rationalFromBig(num, den)
}

// Sub returns a-b.
func (a Rational) Sub(b Rational) Rational {
	return a.Add(b.Neg())
}

// Neg returns -a.
func (a Rational) Neg() Rational { return Rational{N: -a.N, D: a.D} }

// Abs returns the absolute value.
func (a Rational) Abs() Rational {
	if a.N < 0 {
		return a.Neg()
	}
	return a
}

// Cmp returns -1, 0 or 1 when a is less than, equal to or greater than b.
func (a Rational) Cmp(b Rational) int {
	l := new(big.Int).Mul(big.NewInt(a.N), big.NewInt(b.D))
	r := new(big.Int).Mul(big.NewInt(b.N), big.NewInt(a.D))
	return l.Cmp(r)
}

// Equal reports whether a and b represent the same rational value.
func (a Rational) Equal(b Rational) bool { return a.Cmp(b) == 0 }

// IsIntegral reports whether the value is an integer.
func (a Rational) IsIntegral() bool { return a.N%a.D == 0 }

// Int returns the rational as an integer when integral.
func (a Rational) Int() (int64, bool) {
	if !a.IsIntegral() {
		return 0, false
	}
	return a.N / a.D, true
}

// String renders the exact rational.
func (a Rational) String() string {
	if a.D == 1 {
		return fmt.Sprintf("%d", a.N)
	}
	return fmt.Sprintf("%d/%d", a.N, a.D)
}

// MarshalJSON emits a stable string while retaining structured fields.
func (a Rational) MarshalJSON() ([]byte, error) {
	type alias Rational
	return json.Marshal(struct {
		alias
		Text string `json:"text"`
	}{alias: alias(a), Text: a.String()})
}

func rationalFromBig(n, d *big.Int) Rational {
	if d.Sign() == 0 {
		panic("clock: zero rational denominator")
	}
	if d.Sign() < 0 {
		n.Neg(n)
		d.Neg(d)
	}
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(n), d)
	if g.Sign() != 0 {
		n.Quo(n, g)
		d.Quo(d, g)
	}
	if !n.IsInt64() || !d.IsInt64() {
		panic("clock: rational result exceeds int64")
	}
	return Rational{N: n.Int64(), D: d.Int64()}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
