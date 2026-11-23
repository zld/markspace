// Package clock provides the shared exact-time foundation of the media
// pipeline: rational arithmetic, deterministic timescale conversion and the
// common error model.
package clock

import "math/big"

// Rational is an exact rational number kept in lowest terms with a positive
// denominator.
type Rational struct {
	Num int64
	Den int64
}

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// NewRational returns num/den normalized to lowest terms. It panics on a
// zero denominator.
func NewRational(num, den int64) Rational {
	if den == 0 {
		panic("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(num, den)
	return Rational{Num: num / g, Den: den / g}
}

func (r Rational) rat() *big.Rat { return big.NewRat(r.Num, r.Den) }

func fromRat(v *big.Rat) Rational {
	return Rational{Num: v.Num().Int64(), Den: v.Denom().Int64()}
}

// Add returns r+o.
func (r Rational) Add(o Rational) Rational {
	return fromRat(new(big.Rat).Add(r.rat(), o.rat()))
}

// Sub returns r-o.
func (r Rational) Sub(o Rational) Rational {
	return fromRat(new(big.Rat).Sub(r.rat(), o.rat()))
}

// Mul returns r*o.
func (r Rational) Mul(o Rational) Rational {
	return fromRat(new(big.Rat).Mul(r.rat(), o.rat()))
}

// Cmp compares r and o, returning -1, 0 or +1.
func (r Rational) Cmp(o Rational) int { return r.rat().Cmp(o.rat()) }

// Float64 returns the nearest float64 value.
func (r Rational) Float64() float64 {
	f, _ := r.rat().Float64()
	return f
}

// RoundMode selects the rounding direction of timescale conversion.
type RoundMode int

const (
	// RoundNearest rounds to the nearest tick, half away from zero.
	RoundNearest RoundMode = iota
	// RoundDown rounds toward negative infinity.
	RoundDown
	// RoundUp rounds toward positive infinity.
	RoundUp
)

var bigOne = big.NewInt(1)

// MulInt64 computes r*v with exact arithmetic and rounds to an integer
// using mode.
func MulInt64(r Rational, v int64, mode RoundMode) int64 {
	n := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(v))
	d := big.NewInt(r.Den)
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, d, rem) // truncated toward zero
	if rem.Sign() == 0 {
		return q.Int64()
	}
	switch mode {
	case RoundDown:
		if n.Sign() < 0 {
			q.Sub(q, bigOne)
		}
	case RoundUp:
		if n.Sign() > 0 {
			q.Add(q, bigOne)
		}
	case RoundNearest:
		twice := new(big.Int).Lsh(new(big.Int).Abs(rem), 1)
		if twice.Cmp(d) >= 0 {
			if n.Sign() < 0 {
				q.Sub(q, bigOne)
			} else {
				q.Add(q, bigOne)
			}
		}
	}
	return q.Int64()
}

// ConvertTicks converts value ticks from one timescale to another using
// exact arithmetic and the requested rounding mode.
func ConvertTicks(value int64, from, to uint32, mode RoundMode) int64 {
	if from == 0 || to == 0 {
		panic("clock: zero timescale")
	}
	return MulInt64(NewRational(value, int64(from)), int64(to), mode)
}

// Seconds expresses ticks at the given timescale as a rational number of
// seconds.
func Seconds(ticks int64, timescale uint32) Rational {
	return NewRational(ticks, int64(timescale))
}
