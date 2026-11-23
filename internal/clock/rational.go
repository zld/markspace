// Package clock implements exact rational time conversion.
//
// All timeline arithmetic in the forensic pipeline uses rational numbers so
// that no floating-point rounding can accumulate across tracks. Every
// operation reports overflow explicitly instead of wrapping silently.
package clock

import (
	"errors"
	"fmt"
	"math"
)

// Rational is an exact signed rational number Num/Den with Den > 0.
type Rational struct {
	num int64
	den int64 // always positive
}

// ErrOverflow is returned when an arithmetic operation cannot be represented
// in int64 without wrapping.
var ErrOverflow = errors.New("clock: rational arithmetic overflow")

// New returns the reduced rational n/d. It panics on a zero denominator;
// a zero denominator is a programmer error, not recoverable input damage.
func New(n, d int64) Rational {
	if d == 0 {
		panic("clock: zero denominator")
	}
	if d < 0 {
		n, d = -n, -d
	}
	g := gcd(abs64(n), d)
	if g > 1 {
		n /= g
		d /= g
	}
	return Rational{num: n, den: d}
}

// Num returns the numerator.
func (r Rational) Num() int64 { return r.num }

// Den returns the denominator (always positive).
func (r Rational) Den() int64 { return r.den }

// Zero is the rational value 0.
var Zero = Rational{num: 0, den: 1}

// Add returns r+o or ErrOverflow when the reduced result overflows int64.
func (r Rational) Add(o Rational) (Rational, error) {
	// n1/d1 + n2/d2 = (n1*d2 + n2*d1) / (d1*d2)
	d1, d2 := r.den, o.den
	if mulOverflows(r.num, d2) || mulOverflows(o.num, d1) || mulOverflows(d1, d2) {
		return Zero, ErrOverflow
	}
	num := r.num*d2 + o.num*d1
	if addOverflows(r.num*d2, o.num*d1) {
		return Zero, ErrOverflow
	}
	return New(num, d1*d2), nil
}

// Sub returns r-o or ErrOverflow on int64 overflow.
func (r Rational) Sub(o Rational) (Rational, error) {
	// Negating MinInt64 is not representable in int64.
	if o.num == math.MinInt64 {
		return Zero, ErrOverflow
	}
	return r.Add(Rational{num: -o.num, den: o.den})
}

// Mul returns r*o or ErrOverflow on int64 overflow.
func (r Rational) Mul(o Rational) (Rational, error) {
	if mulOverflows(r.num, o.num) || mulOverflows(r.den, o.den) {
		return Zero, ErrOverflow
	}
	return New(r.num*o.num, r.den*o.den), nil
}

// Cmp returns -1, 0 or 1 as r is less than, equal to, or greater than o.
// It returns an error only on overflow; callers may treat that as fatal for
// the affected track.
func (r Rational) Cmp(o Rational) (int, error) {
	if mulOverflows(r.num, o.den) || mulOverflows(o.num, r.den) {
		return 0, ErrOverflow
	}
	l, rr := r.num*o.den, o.num*r.den
	switch {
	case l < rr:
		return -1, nil
	case l > rr:
		return 1, nil
	default:
		return 0, nil
	}
}

// IsZero reports whether the value is zero.
func (r Rational) IsZero() bool { return r.num == 0 }

// IsNegative reports whether the value is strictly negative.
func (r Rational) IsNegative() bool { return r.num < 0 }

// Milliseconds returns the value in milliseconds as a float64. Conversion to
// float happens only at reporting boundaries, never inside timeline math.
func (r Rational) Milliseconds() float64 {
	return float64(r.num) * 1000.0 / float64(r.den)
}

// ScaleUnits converts a tick count expressed in source ticks-per-second into
// the timeline base (seconds, denominator 1) as an exact rational.
// It returns ErrOverflow when the multiplication does not fit in int64.
func ScaleUnits(ticks, ticksPerSecond int64) (Rational, error) {
	if ticksPerSecond <= 0 {
		return Zero, fmt.Errorf("clock: non-positive time base %d", ticksPerSecond)
	}
	if mulOverflows(ticks, 1) {
		return Zero, ErrOverflow
	}
	return New(ticks, ticksPerSecond), nil
}

// ToTicks converts the rational into an integer tick count in the given
// destination time base, truncating toward zero. It returns ErrOverflow when
// the destination tick count cannot be represented in int64.
func (r Rational) ToTicks(ticksPerSecond int64) (int64, error) {
	if ticksPerSecond <= 0 {
		return 0, fmt.Errorf("clock: non-positive time base %d", ticksPerSecond)
	}
	if mulOverflows(r.num, ticksPerSecond) {
		return 0, ErrOverflow
	}
	return (r.num * ticksPerSecond) / r.den, nil
}

// String renders the fraction for deterministic logs and manifests.
func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.num, r.den)
}

// MarshalText makes Rational emit stable text inside JSON manifests.
func (r Rational) MarshalText() ([]byte, error) {
	return []byte(r.String()), nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}

func mulOverflows(a, b int64) bool {
	if a == 0 || b == 0 {
		return false
	}
	if a == math.MinInt64 {
		// Only multiplication by 1 is representable; -1 would negate MinInt64.
		return b != 1
	}
	if b == math.MinInt64 {
		return a != 1
	}
	q := a * b
	return q/a != b
}

func addOverflows(a, b int64) bool {
	if (a > 0 && b > 0 && a > math.MaxInt64-b) ||
		(a < 0 && b < 0 && a < math.MinInt64-b) {
		return true
	}
	return false
}
