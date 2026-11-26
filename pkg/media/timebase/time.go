// Package timebase provides exact rational time arithmetic shared by every
// representation and by the wall-clock RTP anchor.
//
// The shared reference clock is measured in whole milliseconds: one clock
// tick is 1 ms. Representation-local media time runs in the track timescale
// declared by the init segment (mdhd/timescale, e.g. 90000 ticks per second).
// All conversions between the two domains go through Rat so that no precision
// is lost until the final rounding to an integer tick.
package timebase

import (
	"errors"
	"math"
)

// Millisecond is the numerator of the shared-clock unit: 1/1000 s.
const Millisecond int64 = 1

// SharedClockHz is the shared reference clock frequency in hertz.
const SharedClockHz int64 = 1000

// Rat is a signed rational number Num/Den with a strictly positive Den.
// Values are kept normalized (gcd reduced, Den > 0) so that equality and
// ordering reduce to integer comparisons.
type Rat struct {
	num int64
	den int64 // always positive
}

var (
	errZeroDenominator = errors.New("timebase: zero denominator")
	errOverflow        = errors.New("timebase: rational overflow")
)

// NewRat builds a normalized rational. Den must not be zero.
func NewRat(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, errZeroDenominator
	}
	if num == 0 {
		return Rat{num: 0, den: 1}, nil
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd64(abs64(num), den)
	return Rat{num: num / g, den: den / g}, nil
}

// MustNewRat is NewRat that panics on error; it is intended for compile-time
// known constants such as 1/1000.
func MustNewRat(num, den int64) Rat {
	r, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Num returns the numerator.
func (r Rat) Num() int64 { return r.num }

// Den returns the denominator (always positive; the zero value reports 1).
func (r Rat) Den() int64 {
	if r.den == 0 {
		return 1
	}
	return r.den
}

// IsZero reports whether the value equals zero.
func (r Rat) IsZero() bool { return r.num == 0 }

// Float64 returns a floating-point approximation, used only for reporting.
func (r Rat) Float64() float64 {
	return float64(r.Num()) / float64(r.Den())
}

// Add returns r+o without overflowing the intermediate products.
func (r Rat) Add(o Rat) (Rat, error) {
	n1, d1 := r.Num(), r.Den()
	n2, d2 := o.Num(), o.Den()
	g := gcd64(d1, d2)
	// (n1*(d2/g) + n2*(d1/g)) / lcm(d1,d2)
	a, err := mul64(n1, d2/g)
	if err != nil {
		return Rat{}, err
	}
	b, err := mul64(n2, d1/g)
	if err != nil {
		return Rat{}, err
	}
	num, err := add64(a, b)
	if err != nil {
		return Rat{}, err
	}
	den, err := mul64(d1/g, d2)
	if err != nil {
		return Rat{}, err
	}
	return NewRat(num, den)
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) (Rat, error) {
	neg := Rat{num: -o.Num(), den: o.Den()}
	return r.Add(neg)
}

// Mul returns r*o.
func (r Rat) Mul(o Rat) (Rat, error) {
	num, err := mul64(r.Num(), o.Num())
	if err != nil {
		return Rat{}, err
	}
	den, err := mul64(r.Den(), o.Den())
	if err != nil {
		return Rat{}, err
	}
	return NewRat(num, den)
}

// Cmp returns -1, 0 or 1 like bytes.Compare.
func (r Rat) Cmp(o Rat) int {
	// Cross products stay in int64 range for media timelines; guard anyway.
	l, err := mul64(r.Num(), o.Den())
	if err != nil {
		// Fall back to float comparison for pathological magnitudes.
		return compareFloat(r.Float64(), o.Float64())
	}
	right, err := mul64(o.Num(), r.Den())
	if err != nil {
		return compareFloat(r.Float64(), o.Float64())
	}
	switch {
	case l < right:
		return -1
	case l > right:
		return 1
	default:
		return 0
	}
}

// Round returns the nearest integer, halves rounded away from zero.
func (r Rat) Round() (int64, error) {
	n, d := r.Num(), r.Den()
	if n >= 0 {
		q, rem := n/d, n%d
		if rem*2 >= d {
			q++
		}
		return q, nil
	}
	q, rem := (-n)/d, (-n)%d
	if rem*2 >= d {
		q++
	}
	return -q, nil
}

// Floor returns the greatest integer <= r.
func (r Rat) Floor() int64 {
	n, d := r.Num(), r.Den()
	if n >= 0 {
		return n / d
	}
	return -((-n + d - 1) / d)
}

// Scale/ToSharedMs/FromSharedMs/AbsDiff/WithinOneTick live in convert.go.

func compareFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		if v == math.MinInt64 {
			return math.MaxInt64
		}
		return -v
	}
	return v
}

// mul64 multiplies two int64 values with an overflow check.
func mul64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/a != b {
		return 0, errOverflow
	}
	return r, nil
}

// add64 adds two int64 values with an overflow check.
func add64(a, b int64) (int64, error) {
	r := a + b
	if (r^a)&(r^b) < 0 {
		return 0, errOverflow
	}
	return r, nil
}
