// Package timeline provides exact rational time bases and tick conversion.
package timeline

import (
	"fmt"
	"math/big"
)

// Rational is an immutable rational time value. Den is always positive.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// Rat creates a reduced rational. A zero denominator is rejected.
func Rat(num, den int64) Rational {
	if den == 0 {
		panic("timeline: zero rational denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rational{Num: 0, Den: 1}
	}
	g := gcd(abs(num), den)
	return Rational{Num: num / g, Den: den / g}
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// String renders the value as num/den, or num for integral values.
func (r Rational) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%d", r.Num)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// Cmp returns -1, 0, or 1. big.Int keeps comparison exact.
func (r Rational) Cmp(o Rational) int {
	a := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
	b := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
	return a.Cmp(b)
}

// Add returns r+o exactly.
func (r Rational) Add(o Rational) Rational {
	n := new(big.Int).Add(
		new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den)),
		new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den)),
	)
	d := new(big.Int).Mul(big.NewInt(r.Den), big.NewInt(o.Den))
	return bigRatToRational(new(big.Rat).SetFrac(n, d))
}

// Sub returns r-o exactly.
func (r Rational) Sub(o Rational) Rational {
	n := new(big.Int).Sub(
		new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den)),
		new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den)),
	)
	d := new(big.Int).Mul(big.NewInt(r.Den), big.NewInt(o.Den))
	return bigRatToRational(new(big.Rat).SetFrac(n, d))
}

// Mul returns r*o exactly.
func (r Rational) Mul(o Rational) Rational {
	return Rat(r.Num*o.Num, r.Den*o.Den)
}

// Floor returns the greatest integer tick count not greater than r.
func (r Rational) Floor() int64 {
	q, rem := divRem(r.Num, r.Den)
	if rem != 0 && r.Num < 0 {
		return q - 1
	}
	return q
}

// Round returns nearest integer, halves toward positive infinity.
func (r Rational) Round() int64 {
	q, rem := divRem(r.Num, r.Den)
	if rem == 0 {
		return q
	}
	if r.Num >= 0 {
		if 2*rem >= r.Den {
			return q + 1
		}
		return q
	}
	// Go's quotient truncates toward zero; reconstruct Euclidean remainder.
	f := r.Floor()
	frac := r.Sub(Rat(f, 1))
	if frac.Cmp(Rat(1, 2)) >= 0 {
		return f + 1
	}
	return f
}

func divRem(num, den int64) (int64, int64) {
	return num / den, num % den
}

func bigRatToRational(v *big.Rat) Rational {
	return Rat(v.Num().Int64(), v.Denom().Int64())
}

// Clock is a media timescale plus the source's nonzero origin.
type Clock struct {
	Timescale  uint32 `json:"timescale"`
	StartTicks int64  `json:"start_ticks"`
}

// Seconds converts an absolute tick value to exact seconds.
func (c Clock) Seconds(ticks int64) Rational {
	return Rat(ticks, int64(c.Timescale))
}

// NormalizedSeconds removes the declared source origin without mutating source.
func (c Clock) NormalizedSeconds(ticks int64) Rational {
	return Rat(ticks-c.StartTicks, int64(c.Timescale))
}

// Conversion is an exact ticks conversion. Whole is the Euclidean quotient and
// Remainder is numerator over FromScale of one destination tick.
type Conversion struct {
	Whole       int64 `json:"whole"`
	Remainder   int64 `json:"remainder"`
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

// ConvertTicks converts ticks from one timescale to another while retaining
// the division remainder. It never accumulates per-sample rounding error.
func ConvertTicks(ticks int64, fromScale, toScale uint32) Conversion {
	if fromScale == 0 || toScale == 0 {
		panic("timeline: zero timescale")
	}
	num := ticks * int64(toScale)
	den := int64(fromScale)
	whole, rem := divRem(num, den)
	if rem < 0 {
		whole--
		rem += den
	}
	return Conversion{Whole: whole, Remainder: rem, Numerator: num, Denominator: den}
}

// Exact returns the unrounded converted value.
func (c Conversion) Exact() Rational { return Rat(c.Numerator, c.Denominator) }

// ErrorTicks returns absolute rounded-boundary error in destination ticks.
func (c Conversion) ErrorTicks(chosen int64) int64 {
	d := c.Whole - chosen
	if c.Remainder != 0 {
		// Compare chosen with exact quotient, including fractional part.
		exact := c.Exact()
		diff := exact.Sub(Rat(chosen, 1))
		if diff.Num < 0 {
			diff = Rat(-diff.Num, diff.Den)
		}
		return diff.Round()
	}
	if d < 0 {
		return -d
	}
	return d
}
