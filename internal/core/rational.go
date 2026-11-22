// Package core holds the shared media-clock and byte-range primitives used by
// every stage of the mediad pipeline.
package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Rational is an exact media-clock value: Num/Den seconds. Values are always
// kept reduced with a positive denominator so that equal clocks encode to
// identical JSON and therefore to identical report checksums.
type Rational struct {
	Num int64
	Den int64
}

// Rat builds a reduced rational. It panics only on a zero denominator, which
// callers must reject while parsing untrusted input.
func Rat(num, den int64) Rational {
	if den == 0 {
		panic("core: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(absInt(num), den)
	if g == 0 {
		g = 1
	}
	return Rational{Num: num / g, Den: den / g}
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// Zero is the zero clock value.
func Zero() Rational { return Rational{Num: 0, Den: 1} }

// Zero reports whether r is zero.
func (r Rational) Zero() bool { return r.Num == 0 }

// Add returns r+o.
func (r Rational) Add(o Rational) Rational { return Rat(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den) }

// Sub returns r-o.
func (r Rational) Sub(o Rational) Rational { return Rat(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den) }

// Mul returns r*o.
func (r Rational) Mul(o Rational) Rational { return Rat(r.Num*o.Num, r.Den*o.Den) }

// Div returns r/o. It panics when o is zero.
func (r Rational) Div(o Rational) Rational { return Rat(r.Num*o.Den, r.Den*o.Num) }

// Neg returns -r.
func (r Rational) Neg() Rational { return Rational{Num: -r.Num, Den: r.Den} }

// Abs returns |r|.
func (r Rational) Abs() Rational {
	if r.Num < 0 {
		return r.Neg()
	}
	return r
}

// Cmp compares r and o, returning -1, 0 or 1.
func (r Rational) Cmp(o Rational) int {
	l := r.Num * o.Den
	rr := o.Num * r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// Float64 returns the clock as seconds. It is used only for human-facing
// thresholds, never for canonical report values.
func (r Rational) Float64() float64 { return float64(r.Num) / float64(r.Den) }

// String renders the exact reduced fraction.
func (r Rational) String() string {
	return strconv.FormatInt(r.Num, 10) + "/" + strconv.FormatInt(r.Den, 10)
}

// MarshalText encodes the rational as "num/den"; JSON therefore stores a string.
func (r Rational) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText decodes "num/den".
func (r *Rational) UnmarshalText(text []byte) error {
	s := string(text)
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 {
		return fmt.Errorf("core: malformed rational %q", s)
	}
	num, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return fmt.Errorf("core: bad rational numerator: %w", err)
	}
	den, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil {
		return fmt.Errorf("core: bad rational denominator: %w", err)
	}
	if den == 0 {
		return errors.New("core: zero rational denominator")
	}
	*r = Rat(num, den)
	return nil
}

// FromMillis converts integer milliseconds to a reduced rational.
func FromMillis(ms int64) Rational { return Rat(ms, 1000) }

// FromMicros converts integer microseconds to a reduced rational.
func FromMicros(us int64) Rational { return Rat(us, 1000000) }
