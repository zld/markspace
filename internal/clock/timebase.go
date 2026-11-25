// Package clock defines the single rational media timebase used by every
// subsystem and provides deterministic conversions between tick rates.
//
// All media timestamps are represented as integer ticks counted in a rational
// tick rate (Num/Den ticks per second). Conversions use
// round-half-away-from-zero on exact rational arithmetic, so repeated runs
// over the same inputs produce bit-identical results on every platform.
package clock

import (
	"fmt"
	"math/big"
)

// Rational is a reduced rational number with a strictly positive denominator.
type Rational struct {
	Num int64
	Den int64
}

// Master is the single program timebase (90 kHz, MPEG style). Every timeline
// exposed in a manifest is expressed in Master ticks.
var Master = Rational{Num: 90000, Den: 1}

// Rate builds a tick-rate base from an integer rate in Hz (for example a
// sample rate or a timescale).
func Rate(hz uint32) Rational {
	return Rational{Num: int64(hz), Den: 1}
}

// New builds a normalized rational. The denominator must not be zero.
func New(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, fmt.Errorf("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rational{Num: 0, Den: 1}, nil
	}
	g := gcd(abs(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

// MustNew is New for package-level constants and tests.
func MustNew(num, den int64) Rational {
	r, err := New(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// String renders the base as "num/den".
func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// Equal reports rational equality. Values produced by New are always reduced,
// so direct field comparison is sufficient.
func (r Rational) Equal(o Rational) bool {
	return r.Num == o.Num && r.Den == o.Den
}

// Cmp compares r and o, returning -1, 0 or 1.
func (r Rational) Cmp(o Rational) int {
	// Cross multiply in 128-bit space via big.Int to remain exact.
	a := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
	b := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
	return a.Cmp(b)
}

// Float64 returns the base as a tick rate in floating point. It is for
// reporting only; never use it for timeline arithmetic.
func (r Rational) Float64() float64 {
	return float64(r.Num) / float64(r.Den)
}

func (r Rational) valid() bool {
	return r.Den > 0 && r.Num > 0
}

// Validate checks that the rational can be used as a timebase.
func (r Rational) Validate() error {
	if !r.valid() {
		return fmt.Errorf("clock: invalid timebase %s", r.String())
	}
	return nil
}

// ConvertTicks converts a tick count from one base to another using exact
// rational arithmetic and round-half-away-from-zero.
func ConvertTicks(ticks int64, from, to Rational) (int64, error) {
	if err := from.Validate(); err != nil {
		return 0, err
	}
	if err := to.Validate(); err != nil {
		return 0, err
	}
	// ticks * (to_rate / from_rate)
	num := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(to.Num))
	num.Mul(num, big.NewInt(from.Den))
	den := new(big.Int).Mul(big.NewInt(to.Den), big.NewInt(from.Num))
	if den.Sign() == 0 {
		return 0, fmt.Errorf("clock: conversion to zero-rate base %s", to.String())
	}
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	// Round half away from zero: |rem| * 2 >= |den|.
	twice := new(big.Int).Abs(rem)
	twice.Lsh(twice, 1)
	if twice.Cmp(new(big.Int).Abs(den)) >= 0 {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("clock: tick conversion overflows int64 (%d ticks %s->%s)", ticks, from.String(), to.String())
	}
	return q.Int64(), nil
}

// Time is an instant or duration expressed in Base.
type Time struct {
	Ticks int64
	Base  Rational
}

// NewTime constructs a Time after validating the base.
func NewTime(ticks int64, base Rational) (Time, error) {
	if err := base.Validate(); err != nil {
		return Time{}, err
	}
	return Time{Ticks: ticks, Base: base}, nil
}

// Convert re-expresses t in base using deterministic rounding.
func (t Time) Convert(base Rational) (Time, error) {
	ticks, err := ConvertTicks(t.Ticks, t.Base, base)
	if err != nil {
		return Time{}, err
	}
	return Time{Ticks: ticks, Base: base}, nil
}

// Seconds returns the floating-point duration, for reporting only.
func (t Time) Seconds() float64 {
	return float64(t.Ticks) / t.Base.Float64()
}

// Add adds two times, converting o into t's base. Overflow is an error.
func (t Time) Add(o Time) (Time, error) {
	ticks, err := ConvertTicks(o.Ticks, o.Base, t.Base)
	if err != nil {
		return Time{}, err
	}
	sum, ok := addInt64(t.Ticks, ticks)
	if !ok {
		return Time{}, fmt.Errorf("clock: time addition overflows int64")
	}
	return Time{Ticks: sum, Base: t.Base}, nil
}

// Sub subtracts o from t, converting o into t's base.
func (t Time) Sub(o Time) (Time, error) {
	ticks, err := ConvertTicks(o.Ticks, o.Base, t.Base)
	if err != nil {
		return Time{}, err
	}
	diff, ok := subInt64(t.Ticks, ticks)
	if !ok {
		return Time{}, fmt.Errorf("clock: time subtraction overflows int64")
	}
	return Time{Ticks: diff, Base: t.Base}, nil
}

// FromFrames converts an absolute sample/frame position at sampleRate into out.
func FromFrames(frames int64, sampleRate uint32, out Rational) (Time, error) {
	if sampleRate == 0 {
		return Time{}, fmt.Errorf("clock: zero sample rate")
	}
	from := Rational{Num: int64(sampleRate), Den: 1}
	ticks, err := ConvertTicks(frames, from, out)
	if err != nil {
		return Time{}, err
	}
	return Time{Ticks: ticks, Base: out}, nil
}

func addInt64(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

func subInt64(a, b int64) (int64, bool) {
	if b == -1<<63 {
		if a >= 0 {
			return 0, false
		}
		return a - b, true
	}
	return addInt64(a, -b)
}
