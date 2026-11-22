package model

import (
	"errors"
	"math/bits"
)

// Rat is an exact integer rational number (Num/Den) with a positive
// denominator. All timeline math uses Rat so timescale conversion never
// accumulates floating point error.
type Rat struct {
	Num int64
	Den uint64
}

var errBadRat = errors.New("invalid rational: non-positive denominator")

// NewRat builds a normalized rational. Den must be strictly positive.
func NewRat(num int64, den uint64) (Rat, error) {
	if den == 0 {
		return Rat{}, errBadRat
	}
	r := Rat{Num: num, Den: den}
	return r.Canonical(), nil
}

// Canonical reduces the fraction by its greatest common divisor and keeps
// the sign on the numerator.
func (r Rat) Canonical() Rat {
	if r.Num == 0 {
		return Rat{Num: 0, Den: 1}
	}
	neg := r.Num < 0
	u := uint64(r.Num)
	if neg {
		u = uint64(-r.Num)
	}
	g := gcd(u, r.Den)
	r2 := Rat{Num: int64(u / g), Den: r.Den / g}
	if neg {
		r2.Num = -r2.Num
	}
	return r2
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// LCM returns the least common multiple of two positive integers, returning
// false on overflow. Used to pick a common media timescale.
func LCM(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, false
	}
	g := gcd(a, b)
	ag := a / g
	hi, lo := bits.Mul64(ag, b)
	if hi != 0 {
		return 0, false
	}
	return lo, true
}

// Mul scales r by integer n, returning false on int64/uint64 overflow.
func (r Rat) Mul(n int64) (Rat, bool) {
	hi, lo := bits.Mul64(abs64(r.Num), uint64(abs64(n)))
	if hi != 0 {
		return Rat{}, false
	}
	out := Rat{Num: int64(lo), Den: r.Den}
	if (r.Num < 0) != (n < 0) {
		out.Num = -out.Num
	}
	return out.Canonical(), true
}

// Cmp returns -1, 0 or 1 like strings.Compare.
func (r Rat) Cmp(o Rat) int {
	// Compare r.Num/r.Den vs o.Num/o.Den with overflow-safe cross products.
	lh, ll := bits.Mul64(abs64(r.Num), o.Den)
	rh, rl := bits.Mul64(abs64(o.Num), r.Den)
	ls, rs := r.Num < 0, o.Num < 0
	if ls != rs {
		if ls {
			return -1
		}
		return 1
	}
	sign := 1
	if ls {
		sign = -1
	}
	if lh != rh {
		if lh < rh {
			return -sign
		}
		return sign
	}
	if ll < rl {
		return -sign
	}
	if ll > rl {
		return sign
	}
	return 0
}

// Floor returns the greatest integer <= r.
func (r Rat) Floor() int64 {
	q := uint64(r.Num) / r.Den
	if r.Num >= 0 {
		return int64(q)
	}
	rem := uint64(-r.Num) % r.Den
	n := -int64(q)
	if rem != 0 {
		n--
	}
	return n
}

// IsZero reports whether the rational equals zero.
func (r Rat) IsZero() bool { return r.Num == 0 }

// String renders Num/Den (or just Num when Den is 1).
func (r Rat) String() string {
	if r.Den == 1 {
		return itoa(r.Num)
	}
	return itoa(r.Num) + "/" + utoa(r.Den)
}

func abs64(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

func itoa(v int64) string  { return signitoa(v) }
func utoa(v uint64) string { return signitoa(int64(v)) }

func signitoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	neg := v < 0
	u := uint64(v)
	if neg {
		u = uint64(-v)
	}
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
