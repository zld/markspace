// Package timebase defines the single integer time base shared by the
// scanner, planner, worker, publisher and verifier.
//
// All media durations are represented either as an exact integer/rational
// pair in a stream's native clock or as whole nanoseconds. Conversions go
// through math/big so that overflow is detected rather than wrapped.
package timebase

import (
	"errors"
	"math"
	"math/big"
)

// Rate is the canonical media clock used for RTP time audit packets and for
// cross-representation duration comparison: 90 kHz ticks.
const Rate uint64 = 90000

// Rational is an exact non-negative time value expressed as Num/Den in an
// arbitrary stream clock. Den must always be positive.
type Rational struct {
	Num uint64
	Den uint64
}

var errOverflow = errors.New("timebase: integer overflow")

// New builds a Rational, rejecting a zero denominator.
func New(num, den uint64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("timebase: zero denominator")
	}
	return Rational{Num: num, Den: den}, nil
}

// MustNew panics on an invalid rational; it is intended for constants.
func MustNew(num, den uint64) Rational {
	r, err := New(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Cmp returns -1, 0 or +1 like bytes.Compare.
func (r Rational) Cmp(o Rational) int {
	a := new(big.Int).Mul(new(big.Int).SetUint64(r.Num), new(big.Int).SetUint64(o.Den))
	b := new(big.Int).Mul(new(big.Int).SetUint64(o.Num), new(big.Int).SetUint64(r.Den))
	return a.Cmp(b)
}

// Equal reports whether two rationals denote the same instant.
func (r Rational) Equal(o Rational) bool { return r.Cmp(o) == 0 }

// Scale converts r into the target clock (value in target-den units per
// second), rounding to nearest with halves rounded up.
func (r Rational) Scale(targetDen uint64) (uint64, error) {
	if targetDen == 0 {
		return 0, errors.New("timebase: zero target denominator")
	}
	num := new(big.Int).Mul(
		new(big.Int).SetUint64(r.Num),
		new(big.Int).SetUint64(targetDen),
	)
	den := new(big.Int).SetUint64(r.Den)
	num2 := new(big.Int).Add(num, new(big.Int).Quo(den, big.NewInt(2)))
	q := new(big.Int).Quo(num2, den)
	if !q.IsUint64() || q.Uint64() > math.MaxInt64 {
		return 0, errOverflow
	}
	return q.Uint64(), nil
}

// Nanoseconds converts the rational instant into whole nanoseconds.
func (r Rational) Nanoseconds() (uint64, error) {
	return r.Scale(1_000_000_000)
}

// Ticks converts the rational instant into canonical 90 kHz ticks.
func (r Rational) Ticks() (uint64, error) {
	return r.Scale(Rate)
}

// FromTicks turns canonical 90 kHz ticks into the given clock.
func FromTicks(ticks, den uint64) (Rational, error) {
	r, err := New(ticks, Rate)
	if err != nil {
		return Rational{}, err
	}
	return r.rescaleDen(den)
}

func (r Rational) rescaleDen(newDen uint64) (Rational, error) {
	if newDen == 0 {
		return Rational{}, errors.New("timebase: zero denominator")
	}
	num := new(big.Int).Mul(
		new(big.Int).SetUint64(r.Num),
		new(big.Int).SetUint64(newDen),
	)
	q := new(big.Int).Quo(num, new(big.Int).SetUint64(r.Den))
	if !q.IsUint64() || q.Uint64() > math.MaxInt64 {
		return Rational{}, errOverflow
	}
	return Rational{Num: q.Uint64(), Den: newDen}, nil
}

// Add returns r+o, retaining r's denominator. An error is returned when the
// numerator would overflow an int64.
func (r Rational) Add(o Rational) (Rational, error) {
	if r.Den == 0 || o.Den == 0 {
		return Rational{}, errors.New("timebase: zero denominator")
	}
	a := new(big.Int).Mul(new(big.Int).SetUint64(r.Num), new(big.Int).SetUint64(o.Den))
	b := new(big.Int).Mul(new(big.Int).SetUint64(o.Num), new(big.Int).SetUint64(r.Den))
	sum := new(big.Int).Add(a, b)
	num := new(big.Int).Quo(sum, new(big.Int).SetUint64(o.Den))
	if !num.IsUint64() || num.Uint64() > math.MaxInt64 {
		return Rational{}, errOverflow
	}
	return Rational{Num: num.Uint64(), Den: r.Den}, nil
}

// Duration is an exact duration: Count units of Increment in clock Den.
type Duration struct {
	Count     uint64
	Increment Rational
}

// Nanoseconds expresses the duration in whole nanoseconds.
func (d Duration) Nanoseconds() (uint64, error) {
	total, err := d.Increment.Scale(1_000_000_000)
	if err != nil {
		return 0, err
	}
	p := new(big.Int).Mul(new(big.Int).SetUint64(total), new(big.Int).SetUint64(d.Count))
	if !p.IsUint64() || p.Uint64() > math.MaxInt64 {
		return 0, errOverflow
	}
	return p.Uint64(), nil
}

// Ticks expresses the duration in canonical 90 kHz ticks.
func (d Duration) Ticks() (uint64, error) {
	per, err := d.Increment.Ticks()
	if err != nil {
		return 0, err
	}
	p := new(big.Int).Mul(new(big.Int).SetUint64(per), new(big.Int).SetUint64(d.Count))
	if !p.IsUint64() || p.Uint64() > math.MaxInt64 {
		return 0, errOverflow
	}
	return p.Uint64(), nil
}

// OneSourceTick is the tolerance used when comparing representation
// durations: representations may differ by at most a single source clock
// tick, never more.
func OneSourceTick(den uint64) Rational { return Rational{Num: 1, Den: den} }
