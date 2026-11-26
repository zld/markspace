// Package model holds the shared integer-rational media time base and the
// error model used by every processing subsystem.
package model

import (
	"errors"
	"fmt"
	"math/big"
	"math/bits"
)

// ErrInexact marks a conversion that cannot be represented as a whole number
// of ticks in the destination time base. Callers must never round silently.
var ErrInexact = errors.New("inexact time conversion")

// Rate is an exact, always-positive rational number of ticks per second.
// A media timescale of 90000 is Rate{Num: 90000, Den: 1}.
type Rate struct {
	Num int64
	Den int64
}

// NewRate builds a rate from an integer timescale (ticks per second).
func NewRate(timescale uint32) (Rate, error) {
	if timescale == 0 {
		return Rate{}, fmt.Errorf("time base: zero timescale")
	}
	return Rate{Num: int64(timescale), Den: 1}, nil
}

// NewRateRatio builds Num/Den ticks per second.
func NewRateRatio(num, den int64) (Rate, error) {
	if num <= 0 || den <= 0 {
		return Rate{}, fmt.Errorf("time base: non-positive rate %d/%d", num, den)
	}
	return reduceRate(Rate{Num: num, Den: den}), nil
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

func lcm64(a, b int64) (int64, error) {
	if a <= 0 || b <= 0 {
		return 0, fmt.Errorf("time base: lcm of non-positive values %d,%d", a, b)
	}
	g := gcd64(a, b)
	hi, lo := bits.Mul64(uint64(a/g), uint64(b))
	if hi != 0 || lo > uint64(1<<62) {
		return 0, fmt.Errorf("time base: lcm overflow for %d,%d", a, b)
	}
	return int64(lo), nil
}

func reduceRate(r Rate) Rate {
	g := gcd64(r.Num, r.Den)
	return Rate{Num: r.Num / g, Den: r.Den / g}
}

// Valid reports whether the rate is a positive rational.
func (r Rate) Valid() bool { return r.Num > 0 && r.Den > 0 }

// Reduced returns the rate in lowest terms.
func (r Rate) Reduced() Rate { return reduceRate(r) }

// String renders the rate for diagnostics.
func (r Rate) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// TicksPerSecond returns the rate as a float, only for display and evidence.
func (r Rate) TicksPerSecond() float64 { return float64(r.Num) / float64(r.Den) }

// UnifyRate returns the smallest exact common rate for all inputs. Converting
// ticks between rates is only accepted when the result is a whole number.
func UnifyRate(rates ...Rate) (Rate, error) {
	if len(rates) == 0 {
		return Rate{}, fmt.Errorf("time base: no rates to unify")
	}
	out := rates[0].Reduced()
	if !out.Valid() {
		return Rate{}, fmt.Errorf("time base: invalid rate %s", out)
	}
	for _, r := range rates[1:] {
		r = r.Reduced()
		if !r.Valid() {
			return Rate{}, fmt.Errorf("time base: invalid rate %s", r)
		}
		den, err := lcm64(out.Den, r.Den)
		if err != nil {
			return Rate{}, err
		}
		a := new(big.Int).Mul(big.NewInt(out.Num), big.NewInt(den/out.Den))
		b := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(den/r.Den))
		num, err := lcm64(a.Int64(), b.Int64())
		if err != nil {
			return Rate{}, err
		}
		out = reduceRate(Rate{Num: num, Den: den})
	}
	return out, nil
}

// Time is a tick count in an explicit rate. Two Time values are comparable
// only after rescaling to a shared rate.
type Time struct {
	Ticks int64
	Rate  Rate
}

// NewTime validates and constructs a timestamp.
func NewTime(ticks int64, rate Rate) (Time, error) {
	if !rate.Valid() {
		return Time{}, fmt.Errorf("time base: invalid rate %s", rate)
	}
	return Time{Ticks: ticks, Rate: rate.Reduced()}, nil
}

// MustTime is NewTime for statically valid rates.
func MustTime(ticks int64, rate Rate) Time { t, _ := NewTime(ticks, rate); return t }

// Rescale converts t into target ticks exactly, failing rather than rounding.
func (t Time) Rescale(target Rate) (Time, error) {
	if !t.Rate.Valid() || !target.Valid() {
		return Time{}, fmt.Errorf("time base: invalid rescale %s -> %s", t.Rate, target)
	}
	target = target.Reduced()
	if t.Rate == target {
		return t, nil
	}
	// ticks * target.Num * t.Rate.Den / (target.Den * t.Rate.Num)
	num := new(big.Int).Mul(big.NewInt(t.Ticks), big.NewInt(target.Num))
	num.Mul(num, big.NewInt(t.Rate.Den))
	den := new(big.Int).Mul(big.NewInt(target.Den), big.NewInt(t.Rate.Num))
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 {
		return Time{}, fmt.Errorf("%w: %d ticks @%s -> @%s", ErrInexact, t.Ticks, t.Rate, target)
	}
	if !quo.IsInt64() {
		return Time{}, fmt.Errorf("time base: rescale overflow %d @%s -> @%s", t.Ticks, t.Rate, target)
	}
	return Time{Ticks: quo.Int64(), Rate: target}, nil
}

// Add returns t+o in t's rate.
func (t Time) Add(o Time) (Time, error) {
	ro, err := o.Rescale(t.Rate)
	if err != nil {
		return Time{}, err
	}
	sum, err := addInt64(t.Ticks, ro.Ticks)
	if err != nil {
		return Time{}, err
	}
	return Time{Ticks: sum, Rate: t.Rate}, nil
}

// Sub returns t-o in t's rate.
func (t Time) Sub(o Time) (Time, error) {
	ro, err := o.Rescale(t.Rate)
	if err != nil {
		return Time{}, err
	}
	if (ro.Ticks > 0 && t.Ticks < -1<<63+ro.Ticks) || (ro.Ticks < 0 && t.Ticks > 1<<63-1+ro.Ticks) {
		return Time{}, fmt.Errorf("time base: subtraction overflow")
	}
	return Time{Ticks: t.Ticks - ro.Ticks, Rate: t.Rate}, nil
}

// Compare returns -1, 0 or 1 without rounding. It compares the exact rational
// instants ticks*Den/Num, so instants that are not representable in the other
// rate still order correctly. It only fails on an invalid rate.
func (t Time) Compare(o Time) (int, error) {
	if !t.Rate.Valid() || !o.Rate.Valid() {
		return 0, fmt.Errorf("time base: invalid compare %s vs %s", t.Rate, o.Rate)
	}
	// t.Ticks * t.Rate.Den * o.Rate.Num  ?  o.Ticks * o.Rate.Den * t.Rate.Num
	l := new(big.Int).Mul(big.NewInt(t.Ticks), big.NewInt(t.Rate.Den))
	l.Mul(l, big.NewInt(o.Rate.Num))
	r := new(big.Int).Mul(big.NewInt(o.Ticks), big.NewInt(o.Rate.Den))
	r.Mul(r, big.NewInt(t.Rate.Num))
	return l.Cmp(r), nil
}

// Seconds is for reporting only; all internal comparisons stay integral.
func (t Time) Seconds() float64 {
	return float64(t.Ticks) * float64(t.Rate.Den) / float64(t.Rate.Num)
}

// Range is a half-open media range [Start, End).
type Range struct {
	Start Time
	End   Time
}

// Validate checks ordering on a shared time base.
func (r Range) Validate() error {
	c, err := r.Start.Compare(r.End)
	if err != nil {
		return err
	}
	if c > 0 {
		return fmt.Errorf("time base: inverted range start=%d end=%d", r.Start.Ticks, r.End.Ticks)
	}
	return nil
}

// SecondsRange reports the range for evidence output.
func (r Range) SecondsRange() (float64, float64) { return r.Start.Seconds(), r.End.Seconds() }

// FromTicks rescales a raw tick count from one rate to another exactly.
func FromTicks(ticks int64, from, to Rate) (int64, error) {
	out, err := (Time{Ticks: ticks, Rate: from.Reduced()}).Rescale(to)
	if err != nil {
		return 0, err
	}
	return out.Ticks, nil
}

func addInt64(a, b int64) (int64, error) {
	if (b > 0 && a > 1<<63-1-b) || (b < 0 && a < -1<<63-b) {
		return 0, fmt.Errorf("time base: addition overflow")
	}
	return a + b, nil
}
