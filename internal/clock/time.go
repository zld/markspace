// Package clock provides exact rational media time and integer-tick
// conversions. No operation accumulates floating point error: callers keep
// exact rational values or integer ticks plus a timescale.
package clock

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Time is an integer number of ticks at a fixed timescale. The zero value is
// invalid; use New or Must.
type Time struct {
	Ticks     int64
	Timescale uint32
}

// ErrInvalidTime is returned for a zero timescale or a malformed decimal.
var ErrInvalidTime = errors.New("invalid media time")

// New builds a Time and rejects a zero timescale.
func New(ticks int64, timescale uint32) (Time, error) {
	if timescale == 0 {
		return Time{}, ErrInvalidTime
	}
	return Time{Ticks: ticks, Timescale: timescale}, nil
}

// Must is New for package constants and tests.
func Must(ticks int64, timescale uint32) Time {
	t, err := New(ticks, timescale)
	if err != nil {
		panic(err)
	}
	return t
}

// Valid reports whether t has a usable timescale.
func (t Time) Valid() bool { return t.Timescale != 0 }

// Rescale converts t to another timescale using round-half-away-from-zero.
// Conversion is a single integer operation, so repeated conversions do not
// introduce per-step drift.
func (t Time) Rescale(timescale uint32) Time {
	if timescale == 0 || t.Timescale == 0 || timescale == t.Timescale {
		return Time{Ticks: t.Ticks, Timescale: timescale}
	}
	return Time{Ticks: roundDiv(t.Ticks*int64(timescale), int64(t.Timescale)), Timescale: timescale}
}

// Rational returns the exact value t.Ticks / t.Timescale.
func (t Time) Rational() Rational {
	return Rational{Num: t.Ticks, Den: int64(t.Timescale)}
}

// String renders the time as seconds plus the source ticks.
func (t Time) String() string {
	if !t.Valid() {
		return "invalid-time"
	}
	r := t.Rational()
	return fmt.Sprintf("%ss[ticks=%d,timescale=%d]", r.SecondsString(), t.Ticks, t.Timescale)
}

// Compare returns -1, 0 or 1 comparing t with o exactly.
func (t Time) Compare(o Time) int {
	if t.Timescale == o.Timescale {
		return cmpInt64(t.Ticks, o.Ticks)
	}
	// Cross-multiplication stays exact for the tick ranges used by media.
	l := t.Ticks * int64(o.Timescale)
	r := o.Ticks * int64(t.Timescale)
	return cmpInt64(l, r)
}

// Rational is an exact signed fraction. Den is always positive.
type Rational struct {
	Num int64
	Den int64
}

// NewRational normalises num/den. A zero denominator is rejected.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrInvalidTime
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(absInt64(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

// MustRational is NewRational for constants and tests.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// ParseDecimal parses a non-negative HLS-style decimal exactly, e.g.
// "0.500000" -> 1/2. Signs, exponents and empty fractions are rejected.
func ParseDecimal(s string) (Rational, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return Rational{}, fmt.Errorf("%w: decimal %q", ErrInvalidTime, s)
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return Rational{}, fmt.Errorf("%w: decimal %q", ErrInvalidTime, s)
	}
	whole := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if whole == "" || (len(parts) == 2 && frac == "") {
		return Rational{}, fmt.Errorf("%w: decimal %q", ErrInvalidTime, s)
	}
	if !allDigits(whole) || !allDigits(frac) {
		return Rational{}, fmt.Errorf("%w: decimal %q", ErrInvalidTime, s)
	}
	den := int64(1)
	for range frac {
		den *= 10
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return Rational{}, fmt.Errorf("%w: decimal %q: %v", ErrInvalidTime, s, err)
	}
	return NewRational(n, den)
}

// Add returns a+b exactly. The zero value is treated as 0/1.
func (r Rational) Add(o Rational) Rational {
	r, o = r.zeroToOne(), o.zeroToOne()
	return Rational{Num: r.Num*o.Den + o.Num*r.Den, Den: r.Den * o.Den}.normalized()
}

// Sub returns a-b exactly. The zero value is treated as 0/1.
func (r Rational) Sub(o Rational) Rational {
	r, o = r.zeroToOne(), o.zeroToOne()
	return Rational{Num: r.Num*o.Den - o.Num*r.Den, Den: r.Den * o.Den}.normalized()
}

// MulInt multiplies by an integer.
func (r Rational) MulInt(n int64) Rational {
	return Rational{Num: r.Num * n, Den: r.Den}.normalized()
}

// Compare returns -1, 0 or 1 comparing r with o exactly. The zero value is
// treated as 0/1.
func (r Rational) Compare(o Rational) int {
	r, o = r.zeroToOne(), o.zeroToOne()
	return cmpInt64(r.Num*o.Den, o.Num*r.Den)
}

// Ticks converts the exact value to integer ticks, rounding half away from
// zero. This is the only place a rational value quantises.
func (r Rational) Ticks(timescale uint32) int64 {
	r = r.zeroToOne()
	return roundDiv(r.Num*int64(timescale), r.Den)
}

// SecondsString renders a decimal approximation only for human-readable logs.
func (r Rational) SecondsString() string {
	return strconv.FormatFloat(float64(r.Num)/float64(r.Den), 'f', 6, 64)
}

func (r Rational) String() string {
	r = r.zeroToOne()
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// zeroToOne maps the zero value onto 0/1 so it participates in arithmetic.
func (r Rational) zeroToOne() Rational {
	if r.Den == 0 {
		return Rational{Num: 0, Den: 1}
	}
	return r
}

func (r Rational) normalized() Rational {
	if r.Den == 0 {
		return Rational{}
	}
	if r.Den < 0 {
		r.Num, r.Den = -r.Num, -r.Den
	}
	g := gcd(absInt64(r.Num), r.Den)
	if g == 0 {
		return r
	}
	return Rational{Num: r.Num / g, Den: r.Den / g}
}

func roundDiv(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	if den < 0 {
		num, den = -num, -den
	}
	neg := num < 0
	if neg {
		num = -num
	}
	q := num / den
	rem := num % den
	if rem*2 >= den {
		q++
	}
	if neg {
		return -q
	}
	return q
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a <= 0 {
		return 1
	}
	return a
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
