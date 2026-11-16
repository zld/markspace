// Package timebase provides exact rational arithmetic for converting media
// timestamps between integer tick timescales. All cross-timescale comparisons
// use integer rationals so no floating-point rounding can accumulate.
package timebase

import (
	"errors"
	"fmt"
	"math/big"
	"math/bits"
	"strings"
)

// Rational is a signed rational number. Den is always positive.
type Rational struct {
	Num int64
	Den int64
}

// Make returns num/den as a reduced rational. It fails on a zero denominator
// or when the reduced value does not fit int64.
func Make(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("rational: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	return fromBig(big.NewInt(num), big.NewInt(den))
}

func fromBig(num, den *big.Int) (Rational, error) {
	if den.Sign() == 0 {
		return Rational{}, errors.New("rational: zero denominator")
	}
	if den.Sign() < 0 {
		num.Neg(num)
		den.Neg(den)
	}
	if num.Sign() == 0 {
		return Rational{Num: 0, Den: 1}, nil
	}
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(num), new(big.Int).Abs(den))
	num.Quo(num, g)
	den.Quo(den, g)
	if !num.IsInt64() || !den.IsInt64() {
		return Rational{}, fmt.Errorf("rational: value overflow int64: %s/%s", num, den)
	}
	return Rational{Num: num.Int64(), Den: den.Int64()}, nil
}

// IsZero reports whether r equals zero.
func (r Rational) IsZero() bool { return r.Num == 0 }

// Cmp returns -1, 0 or 1 as r is less than, equal to, or greater than o.
func (r Rational) Cmp(o Rational) int {
	a := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
	b := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
	return a.Cmp(b)
}

// Sub returns r-o as a reduced rational.
func (r Rational) Sub(o Rational) (Rational, error) {
	num := new(big.Int).Sub(
		new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den)),
		new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den)))
	den := new(big.Int).Mul(big.NewInt(r.Den), big.NewInt(o.Den))
	return fromBig(num, den)
}

// Abs returns |r|.
func (r Rational) Abs() Rational {
	if r.Num < 0 {
		return Rational{Num: -r.Num, Den: r.Den}
	}
	return r
}

// Mul returns r*n as a reduced rational.
func (r Rational) Mul(n int64) (Rational, error) {
	return fromBig(new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(n)), big.NewInt(r.Den))
}

// TicksToMs converts ticks counted in timescale units to milliseconds.
func TicksToMs(ticks, timescale int64) (Rational, error) {
	if timescale <= 0 {
		return Rational{}, fmt.Errorf("timebase: non-positive timescale %d", timescale)
	}
	r, err := Make(ticks, timescale)
	if err != nil {
		return Rational{}, err
	}
	return r.Mul(1000)
}

// FormatMillis prints r (already expressed in milliseconds) with exactly three
// decimal places, using integer rounding instead of floating point.
func FormatMillis(r Rational) string {
	micros := big.NewInt(r.Num)
	micros.Mul(micros, big.NewInt(1000))
	den := big.NewInt(r.Den)
	q, rem := new(big.Int).QuoRem(micros, den, new(big.Int))
	if rem.Sign() != 0 && new(big.Int).Abs(rem).Mul(new(big.Int).Abs(rem), big.NewInt(2)).Cmp(new(big.Int).Abs(den)) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	neg := q.Sign() < 0
	if neg {
		q.Neg(q)
	}
	whole := new(big.Int).Quo(q, big.NewInt(1000))
	frac := new(big.Int).Mod(q, big.NewInt(1000))
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%s.%03d", sign, whole.String(), frac)
}

// Add64 returns a+b and ok=false on signed overflow.
func Add64(a, b int64) (int64, bool) {
	sum, carry := bits.Add64(uint64(a), uint64(b), 0)
	if (a >= 0 && b >= 0 && int64(sum) < 0) || (a < 0 && b < 0 && int64(sum) >= 0) {
		return 0, false
	}
	_ = carry
	return int64(sum), true
}

// Mul64 returns a*b and ok=false on signed overflow.
func Mul64(a, b int64) (int64, bool) {
	p := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	if !p.IsInt64() {
		return 0, false
	}
	return p.Int64(), true
}

// ParseDuration parses durations such as "20ms", "1.5s" or "250us" into an
// exact millisecond rational.
func ParseDuration(s string) (Rational, error) {
	orig := s
	unit := ""
	for _, u := range []string{"ms", "us", "s"} {
		if strings.HasSuffix(s, u) {
			unit = u
			s = strings.TrimSuffix(s, u)
			break
		}
	}
	if unit == "" || s == "" {
		return Rational{}, fmt.Errorf("invalid duration %q: want a number ending in us, ms or s", orig)
	}
	dot := strings.IndexByte(s, '.')
	intPart, fracPart := s, ""
	if dot >= 0 {
		intPart, fracPart = s[:dot], s[dot+1:]
		if intPart == "" || fracPart == "" || strings.IndexByte(fracPart, '.') >= 0 {
			return Rational{}, fmt.Errorf("invalid duration %q", orig)
		}
	}
	if intPart == "" {
		intPart = "0"
	}
	whole, ok := parseUint(intPart)
	if !ok {
		return Rational{}, fmt.Errorf("invalid duration %q", orig)
	}
	scale := int64(1)
	for i := 0; i < len(fracPart); i++ {
		if fracPart[i] < '0' || fracPart[i] > '9' {
			return Rational{}, fmt.Errorf("invalid duration %q", orig)
		}
		scale *= 10
	}
	frac := int64(0)
	if fracPart != "" {
		frac, ok = parseUint(fracPart)
		if !ok {
			return Rational{}, fmt.Errorf("invalid duration %q", orig)
		}
	}
	num, ok1 := Mul64(whole, scale)
	num2, ok2 := Add64(num, frac)
	if !ok1 || !ok2 {
		return Rational{}, fmt.Errorf("invalid duration %q: overflow", orig)
	}
	r, err := Make(num2, scale)
	if err != nil {
		return Rational{}, err
	}
	switch unit {
	case "s":
		return r.Mul(1000)
	case "ms":
		return r, nil
	case "us":
		return Make(r.Num, r.Den*1000)
	default:
		return Rational{}, fmt.Errorf("invalid duration %q", orig)
	}
}

func parseUint(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		var ok bool
		n, ok = Mul64(n, 10)
		if !ok {
			return 0, false
		}
		n, ok = Add64(n, int64(c-'0'))
		if !ok {
			return 0, false
		}
	}
	return n, true
}
