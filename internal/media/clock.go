package media

import (
	"fmt"
	"math/bits"
)

// Rat is an exact rational number used as the shared cross-track time base.
// The value is always kept normalized: Den > 0 and gcd(|Num|, Den) == 1.
type Rat struct {
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
	return a
}

// mul128 returns the signed 128-bit product a*b as (hi, lo).
func mul128(a, b int64) (hi, lo int64) {
	h, l := bits.Mul64(uint64(a), uint64(b))
	hi = int64(h)
	if a < 0 {
		hi -= b
	}
	if b < 0 {
		hi -= a
	}
	return hi, int64(l)
}

// fitsInt64 reports whether the signed 128-bit value (hi, lo) fits in int64.
func fitsInt64(hi, lo int64) bool { return hi == lo>>63 }

// NewRat builds a normalized rational; Den must be non-zero.
func NewRat(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, &Error{Kind: KindOverflow, Msg: "rational with zero denominator"}
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(num, den)
	return Rat{Num: num / g, Den: den / g}, nil
}

// Add returns r+o with overflow checking.
func (r Rat) Add(o Rat) (Rat, error) {
	h1, l1 := mul128(r.Num, o.Den)
	h2, l2 := mul128(o.Num, r.Den)
	lo, carry := bits.Add64(uint64(l1), uint64(l2), 0)
	hi := h1 + h2 + int64(carry)
	if !fitsInt64(hi, int64(lo)) {
		return Rat{}, &Error{Kind: KindOverflow, Msg: "rational add overflow (numerator)"}
	}
	hd, ld := mul128(r.Den, o.Den)
	if hd != 0 || ld < 0 {
		return Rat{}, &Error{Kind: KindOverflow, Msg: "rational add overflow (denominator)"}
	}
	return NewRat(int64(lo), ld)
}

// Mul returns r*o with overflow checking.
func (r Rat) Mul(o Rat) (Rat, error) {
	// Cross-cancel first to keep values small.
	g1 := gcd(r.Num, o.Den)
	g2 := gcd(o.Num, r.Den)
	a, b := r.Num/g1, o.Den/g1
	c, d := o.Num/g2, r.Den/g2
	hn, ln := mul128(a, c)
	if !fitsInt64(hn, ln) {
		return Rat{}, &Error{Kind: KindOverflow, Msg: "rational multiply overflow (numerator)"}
	}
	hd, ld := mul128(b, d)
	if hd != 0 || ld <= 0 {
		return Rat{}, &Error{Kind: KindOverflow, Msg: "rational multiply overflow (denominator)"}
	}
	return NewRat(ln, ld)
}

// Cmp compares r with o using 128-bit cross multiplication. Denominators are
// positive by invariant, so the sign of the 128-bit difference decides.
func (r Rat) Cmp(o Rat) int {
	h1, l1 := mul128(r.Num, o.Den)
	h2, l2 := mul128(o.Num, r.Den)
	if h1 != h2 {
		if h1 < h2 {
			return -1
		}
		return 1
	}
	if l1 < l2 {
		return -1
	}
	if l1 > l2 {
		return 1
	}
	return 0
}

// Float64 returns the nearest float64 value.
func (r Rat) Float64() float64 { return float64(r.Num) / float64(r.Den) }

func (r Rat) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// Clock maps integer capture ticks to the shared rational time base.
type Clock struct {
	Timescale int64 // ticks per second, must be > 0
}

// NewClock validates the timescale.
func NewClock(timescale int64) (Clock, error) {
	if timescale <= 0 {
		return Clock{}, &Error{Kind: KindRange, Msg: fmt.Sprintf("invalid timescale %d", timescale)}
	}
	return Clock{Timescale: timescale}, nil
}

// ToSeconds converts ticks to seconds exactly.
func (c Clock) ToSeconds(ticks int64) (Rat, error) {
	if c.Timescale <= 0 {
		return Rat{}, &Error{Kind: KindState, Msg: "clock not initialized"}
	}
	return NewRat(ticks, c.Timescale)
}

// Ticks converts a rational time back to ticks, rounding to nearest.
func (c Clock) Ticks(r Rat) (int64, error) {
	scaled, err := r.Mul(Rat{Num: c.Timescale, Den: 1})
	if err != nil {
		return 0, err
	}
	q := scaled.Num / scaled.Den
	rem := scaled.Num % scaled.Den
	if rem*2 >= scaled.Den {
		q++
	} else if rem*2 <= -scaled.Den {
		q--
	}
	return q, nil
}
