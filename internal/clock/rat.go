package clock

import (
	"fmt"
	"math"
)

// Rat is an exact signed rational number used as the single unified time
// base across manifests, MP4 tracks and RTP timestamps. Values are stored
// in reduced form with a positive denominator.
type Rat struct {
	Num int64
	Den int64
}

// NewRat builds a normalized rational, panicking on a zero denominator.
func NewRat(num, den int64) Rat {
	if den == 0 {
		panic("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rat{0, 1}
	}
	g := gcd(abs64(num), den)
	return Rat{num / g, den / g}
}

// FromMillis converts a millisecond count to seconds.
func FromMillis(ms int64) Rat { return NewRat(ms, 1000) }

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Add returns r+o.
func (r Rat) Add(o Rat) Rat {
	return NewRat(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den)
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) Rat {
	return NewRat(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den)
}

// Mul returns r*o.
func (r Rat) Mul(o Rat) Rat {
	return NewRat(r.Num*o.Num, r.Den*o.Den)
}

// Cmp returns -1, 0 or 1 like bytes.Compare.
func (r Rat) Cmp(o Rat) int {
	d := r.Num*o.Den - o.Num*r.Den
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	default:
		return 0
	}
}

// IsZero reports whether the value equals zero.
func (r Rat) IsZero() bool { return r.Num == 0 }

// Seconds renders the value as a float number of seconds.
func (r Rat) Seconds() float64 {
	if r.Den == 0 {
		return math.NaN()
	}
	return float64(r.Num) / float64(r.Den)
}

// RoundMillis quantizes to whole milliseconds for stable reporting.
func (r Rat) RoundMillis() int64 {
	// Round half away from zero.
	v := r.Num * 1000
	if (v >= 0) == (r.Den >= 0) {
		return (v + r.Den/2) / r.Den
	}
	return (v - r.Den/2) / r.Den
}

func (r Rat) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%d", r.Num)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}
