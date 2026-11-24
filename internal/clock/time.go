// Package clock provides exact integer media time handling.
//
// A Time is an integer tick count paired with an explicit timescale (ticks per
// second).  No floating point value is ever used for conversion: comparisons
// are done as rational numbers and rescaling is done with integer division and
// rounding.  This keeps ordering stable and repeatable across tracks whose
// timescales differ.
package clock

import (
	"errors"
	"fmt"
	"math"
	"math/big"
)

// ErrInvalidTimescale is returned when a timescale is zero or cannot be
// represented in a uint32.
var ErrInvalidTimescale = errors.New("clock: invalid timescale")

// Time is an exact media time: Ticks / Timescale seconds.
type Time struct {
	Ticks     int64
	Timescale uint32
}

// New returns a Time.  The caller is responsible for passing a non-zero
// timescale; Valid reports whether the value can be used in arithmetic.
func New(ticks int64, timescale uint32) Time {
	return Time{Ticks: ticks, Timescale: timescale}
}

// Valid reports whether t has a usable timescale.
func (t Time) Valid() bool { return t.Timescale != 0 }

// IsZero reports whether the time is exactly zero (and valid).
func (t Time) IsZero() bool { return t.Timescale != 0 && t.Ticks == 0 }

// String formats the exact rational value without rounding.
func (t Time) String() string {
	return fmt.Sprintf("%d/%d", t.Ticks, t.Timescale)
}

// Cmp compares t and u as rational numbers.  It returns -1, 0 or 1.  Invalid
// timescales are ordered before valid ones and compare equal to each other.
func (t Time) Cmp(u Time) int {
	if !t.Valid() || !u.Valid() {
		switch {
		case !t.Valid() && !u.Valid():
			return 0
		case !t.Valid():
			return -1
		default:
			return 1
		}
	}
	lhs := new(big.Int).Mul(big.NewInt(t.Ticks), new(big.Int).SetUint64(uint64(u.Timescale)))
	rhs := new(big.Int).Mul(big.NewInt(u.Ticks), new(big.Int).SetUint64(uint64(t.Timescale)))
	return lhs.Cmp(rhs)
}

// Rescale converts t to dst ticks.  The exact quotient is rounded half away
// from zero.  The bool result reports whether the conversion was exact (no
// remainder).  Rescaling to the same timescale is always exact.
func (t Time) Rescale(dst uint32) (int64, bool, error) {
	if !t.Valid() || dst == 0 {
		return 0, false, ErrInvalidTimescale
	}
	if dst == t.Timescale {
		return t.Ticks, true, nil
	}
	num := new(big.Int).Mul(big.NewInt(t.Ticks), new(big.Int).SetUint64(uint64(dst)))
	den := new(big.Int).SetUint64(uint64(t.Timescale))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() == 0 {
		if !q.IsInt64() {
			return 0, false, fmt.Errorf("clock: rescale overflow")
		}
		return q.Int64(), true, nil
	}
	// Round half away from zero: compare 2*|r| with den.
	twice := new(big.Int).Abs(r)
	twice.Lsh(twice, 1)
	if twice.Cmp(den) >= 0 {
		if q.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, false, fmt.Errorf("clock: rescale overflow")
	}
	return q.Int64(), false, nil
}

// MustRescale is Rescale without error handling; it panics on invalid input.
func (t Time) MustRescale(dst uint32) int64 {
	v, _, err := t.Rescale(dst)
	if err != nil {
		panic(err)
	}
	return v
}

// Add returns t+u expressed in the least common multiple of their timescales.
// It is intended for durations on the same track.
func (t Time) Add(u Time) (Time, error) {
	ts, err := Unify(t.Timescale, u.Timescale)
	if err != nil {
		return Time{}, err
	}
	a, _, err := t.Rescale(ts)
	if err != nil {
		return Time{}, err
	}
	b, _, err := u.Rescale(ts)
	if err != nil {
		return Time{}, err
	}
	return Time{Ticks: a + b, Timescale: ts}, nil
}

// GCD returns the greatest common divisor of a and b.
func GCD(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// Unify returns the least common multiple of the supplied timescales.  The
// result is the smallest timescale in which every input tick count can be
// represented exactly.  Zero timescales are ignored; an empty list is an error.
func Unify(timescales ...uint32) (uint32, error) {
	var l uint64 = 1
	found := false
	for _, ts := range timescales {
		if ts == 0 {
			continue
		}
		found = true
		g := uint64(GCD(uint32(l), ts))
		next := l / g * uint64(ts)
		if next == 0 || next > math.MaxUint32 {
			return 0, fmt.Errorf("clock: unified timescale overflow")
		}
		l = next
	}
	if !found {
		return 0, ErrInvalidTimescale
	}
	return uint32(l), nil
}
