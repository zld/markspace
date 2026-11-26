// Package core provides the shared rational timebase and the structured
// error model used by every streamcheck subsystem.
package core

import (
	"fmt"
	"strconv"
	"strings"
)

// Rat is an exact rational number. All timestamps and durations in the
// pipeline are represented as Rat so DASH, HLS and RTP timelines can be
// compared without floating point drift.
type Rat struct {
	Num int64
	Den int64
}

// NewRat returns num/den normalized: denominator positive, fully reduced.
// A zero denominator yields the zero value (callers must not pass one).
func NewRat(num, den int64) Rat {
	if den == 0 {
		return Rat{}
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), den)
	return Rat{Num: num / g, Den: den / g}
}

// FromMillis converts milliseconds to a Rat on a 1/1000 timebase.
func FromMillis(ms int64) Rat { return NewRat(ms, 1000) }

func (r Rat) Add(o Rat) Rat { return NewRat(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den) }
func (r Rat) Sub(o Rat) Rat { return NewRat(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den) }
func (r Rat) Mul(o Rat) Rat { return NewRat(r.Num*o.Num, r.Den*o.Den) }

// Cmp returns -1, 0 or 1 comparing r to o.
func (r Rat) Cmp(o Rat) int {
	l, rr := r.Num*o.Den, o.Num*r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	}
	return 0
}

func (r Rat) IsZero() bool { return r.Num == 0 }

// Millis returns the value in milliseconds, truncating toward zero.
// All pipeline timebases divide 1000 evenly, so this is exact in practice.
func (r Rat) Millis() int64 {
	if r.Den == 0 {
		return 0
	}
	return r.Num * 1000 / r.Den
}

// Float returns the nearest float64 value, for reporting only.
func (r Rat) Float() float64 {
	if r.Den == 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Den)
}

func (r Rat) String() string {
	if r.Den == 1 {
		return strconv.FormatInt(r.Num, 10)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// ParseDecimal parses a non-negative decimal such as "2", "2.0" or "6.006"
// into an exact Rat. Used for HLS EXTINF durations.
func ParseDecimal(s string) (Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rat{}, fmt.Errorf("empty decimal")
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	intPart, fracPart, _ := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	for _, c := range intPart + fracPart {
		if c < '0' || c > '9' {
			return Rat{}, fmt.Errorf("invalid decimal %q", s)
		}
	}
	iv, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return Rat{}, fmt.Errorf("invalid decimal %q: %w", s, err)
	}
	num, den := iv, int64(1)
	if fracPart != "" {
		fv, err := strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return Rat{}, fmt.Errorf("invalid decimal %q: %w", s, err)
		}
		for range fracPart {
			den *= 10
		}
		num = iv*den + fv
	}
	if neg {
		num = -num
	}
	return NewRat(num, den), nil
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

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
