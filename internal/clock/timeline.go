// Package clock provides an exact rational-number time base so that
// packet durations and timeline positions never accumulate float error.
package clock

import (
	"fmt"
	"math/bits"
)

// Rational is an exact, always-reduced fraction Num/Den (Den > 0).
type Rational struct {
	Num int64
	Den int64
}

// New returns num/den reduced to lowest terms.
func New(num, den int64) (Rational, error) {
	if den <= 0 {
		return Rational{}, fmt.Errorf("clock: denominator must be positive, got %d", den)
	}
	g := gcd(abs64(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

// Must is New for compile-time constants; it panics on a bad denominator.
func Must(num, den int64) Rational {
	r, err := New(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Add returns r+o or an overflow error.
func (r Rational) Add(o Rational) (Rational, error) {
	a, ok := mul64(r.Num, o.Den)
	if !ok {
		return Rational{}, errOverflow
	}
	b, ok := mul64(o.Num, r.Den)
	if !ok {
		return Rational{}, errOverflow
	}
	n, ok := add64(a, b)
	if !ok {
		return Rational{}, errOverflow
	}
	d, ok := mul64(r.Den, o.Den)
	if !ok {
		return Rational{}, errOverflow
	}
	return New(n, d)
}

// Sub returns r-o.
func (r Rational) Sub(o Rational) (Rational, error) { return r.Add(Rational{-o.Num, o.Den}) }

// Mul returns r*o.
func (r Rational) Mul(o Rational) (Rational, error) {
	n, ok := mul64(r.Num, o.Num)
	if !ok {
		return Rational{}, errOverflow
	}
	d, ok := mul64(r.Den, o.Den)
	if !ok {
		return Rational{}, errOverflow
	}
	return New(n, d)
}

// Cmp returns -1, 0 or 1 comparing r to o.
func (r Rational) Cmp(o Rational) (int, error) {
	a, ok := mul64(r.Num, o.Den)
	if !ok {
		return 0, errOverflow
	}
	b, ok := mul64(o.Num, r.Den)
	if !ok {
		return 0, errOverflow
	}
	switch {
	case a < b:
		return -1, nil
	case a > b:
		return 1, nil
	}
	return 0, nil
}

// IsZero reports whether r == 0.
func (r Rational) IsZero() bool { return r.Num == 0 }

var errOverflow = fmt.Errorf("clock: rational arithmetic overflow")

// Timeline maps integer sample counts onto a rational time base of
// Scale ticks per second.
type Timeline struct{ Scale int64 }

// NewTimeline validates and returns a Timeline for the given sample rate.
func NewTimeline(scale int64) (Timeline, error) {
	if scale <= 0 {
		return Timeline{}, fmt.Errorf("clock: timescale must be positive, got %d", scale)
	}
	return Timeline{Scale: scale}, nil
}

// At returns the exact instant, in seconds, of sample n on the timeline.
func (t Timeline) At(samples int64) Rational { return Must(samples, t.Scale) }

// Duration returns the exact duration, in seconds, of n samples.
func (t Timeline) Duration(samples int64) Rational { return t.At(samples) }

// Quantize converts r seconds to whole ticks of the timeline, rounding to
// nearest, and returns the signed rounding remainder (ticks - exact).
func (t Timeline) Quantize(r Rational) (ticks int64, rem Rational, err error) {
	n, ok := mul64(r.Num, t.Scale)
	if !ok {
		return 0, Rational{}, errOverflow
	}
	q := n / r.Den
	m := n % r.Den
	if m < 0 {
		m += r.Den
		q--
	}
	if 2*m >= r.Den {
		q++
		m -= r.Den
	}
	rem, err = New(m, r.Den*t.Scale)
	if err != nil {
		return 0, Rational{}, err
	}
	return q, rem, nil
}

// AlignError returns the absolute difference, in target-timeline ticks,
// between the exact position srcSamples on src and dstTicks on dst.
func AlignError(src Timeline, srcSamples int64, dst Timeline, dstTicks int64) (int64, error) {
	a := src.At(srcSamples)
	b := dst.At(dstTicks)
	d, err := a.Sub(b)
	if err != nil {
		return 0, err
	}
	if d.Num < 0 {
		d.Num = -d.Num
	}
	n, ok := mul64(d.Num, dst.Scale)
	if !ok {
		return 0, errOverflow
	}
	// round up: any fractional tick of error counts as one
	q := n / d.Den
	if n%d.Den != 0 {
		q++
	}
	return q, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func mul64(a, b int64) (int64, bool) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	// Correct the high word: Mul64 computes the unsigned product.
	if a < 0 {
		hi -= uint64(b)
	}
	if b < 0 {
		hi -= uint64(a)
	}
	if lo>>63 == 0 {
		if hi != 0 {
			return 0, false
		}
	} else if hi != ^uint64(0) {
		return 0, false
	}
	return int64(lo), true
}

func add64(a, b int64) (int64, bool) {
	s := a + b
	if (a > 0 && b > 0 && s <= 0) || (a < 0 && b < 0 && s >= 0) {
		return 0, false
	}
	return s, true
}
