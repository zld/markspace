package clock

import (
	"fmt"
	"math/bits"
)

// Shift is a signed exact time offset. It exists because programme repair often
// needs to pull a backup source back in time (negative offset) while real media
// timestamps themselves are never negative.
type Shift struct {
	N int64
	D int64
}

// MakeShift returns n/d with a positive denominator.
func MakeShift(n, d int64) (Shift, error) {
	if d == 0 {
		return Shift{}, fmt.Errorf("%w: zero shift denominator", ErrOverflow)
	}
	if d < 0 {
		n, d = -n, -d
	}
	g := gcd(n, d)
	if g > 1 {
		n /= g
		d /= g
	}
	return Shift{N: n, D: d}, nil
}

// ShiftZero is no offset.
func ShiftZero() Shift { return Shift{N: 0, D: 1} }

// ShiftBetween returns a-b, which may be negative.
func ShiftBetween(a, b Rational) (Shift, error) {
	x, err := mulChecked(a.N, b.D)
	if err != nil {
		return Shift{}, err
	}
	y, err := mulChecked(b.N, a.D)
	if err != nil {
		return Shift{}, err
	}
	den, err := mulChecked(a.D, b.D)
	if err != nil {
		return Shift{}, err
	}
	return MakeShift(x-y, den)
}

// Apply adds s to t, rejecting a negative result.
func (s Shift) Apply(t Rational) (Rational, error) {
	if s.IsZero() {
		return t, nil
	}
	x, err := mulChecked(t.N, s.D)
	if err != nil {
		return Rational{}, err
	}
	y, err := mulChecked(s.N, t.D)
	if err != nil {
		return Rational{}, err
	}
	den, err := mulChecked(t.D, s.D)
	if err != nil {
		return Rational{}, err
	}
	sum, ok := addSigned(x, y)
	if !ok || sum < 0 {
		return Rational{}, fmt.Errorf("%w: %s applied to %s is negative", ErrOverflow, s, t)
	}
	return Make(sum, den)
}

// Neg returns -s.
func (s Shift) Neg() Shift { return Shift{N: -s.N, D: s.D} }

// IsZero reports whether s is zero.
func (s Shift) IsZero() bool { return s.N == 0 }

// Cmp compares s and o.
func (s Shift) Cmp(o Shift) int {
	x, err := mulChecked(s.N, o.D)
	if err != nil {
		return 0
	}
	y, err := mulChecked(o.N, s.D)
	if err != nil {
		return 0
	}
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}

// Add returns s+o.
func (s Shift) Add(o Shift) (Shift, error) {
	x, err := mulChecked(s.N, o.D)
	if err != nil {
		return Shift{}, err
	}
	y, err := mulChecked(o.N, s.D)
	if err != nil {
		return Shift{}, err
	}
	den, err := mulChecked(s.D, o.D)
	if err != nil {
		return Shift{}, err
	}
	sum, ok := addSigned(x, y)
	if !ok {
		return Shift{}, ErrOverflow
	}
	return MakeShift(sum, den)
}

func (s Shift) String() string {
	if s.D == 0 {
		return "0s"
	}
	return fmt.Sprintf("%d/%ds", s.N, s.D)
}

// mulChecked multiplies two signed 64-bit values, reporting 128-bit overflow.
func mulChecked(a, b int64) (int64, error) {
	neg := (a < 0) != (b < 0)
	ua, ub := uabs(a), uabs(b)
	hi, lo := bits.Mul64(ua, ub)
	limit := uint64(1 << 63)
	if hi != 0 || lo > limit || (neg && lo == limit) {
		return 0, ErrOverflow
	}
	if neg {
		return -int64(lo), nil
	}
	return int64(lo), nil
}

func uabs(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}

// addSigned adds two signed values with overflow detection.
func addSigned(a, b int64) (int64, bool) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}
