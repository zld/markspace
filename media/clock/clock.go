// Package clock provides the shared rational media clock used by every
// subsystem. Conversions between event time (nanoseconds) and per-layer
// media ticks are exact: a reading carries the integer quotient plus the
// exact fractional remainder with its denominator, so no floating point
// approximation ever enters a boundary decision.
package clock

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

var (
	// ErrZeroTimescale is returned when a clock has no usable timescale.
	ErrZeroTimescale = errors.New("clock: timescale must be positive")
	// ErrZeroFrame is returned for a non-positive frame duration.
	ErrZeroFrame = errors.New("clock: frame ticks must be positive")
	// ErrOverflow is returned when an exact conversion would exceed int64.
	ErrOverflow = errors.New("clock: conversion overflow")
	// ErrBadRemainder is returned for a remainder outside [0, den).
	ErrBadRemainder = errors.New("clock: remainder must satisfy 0 <= rem < den")
	// ErrZeroDen is returned for a non-positive rational denominator.
	ErrZeroDen = errors.New("clock: denominator must be positive")
)

// NanosPerSecond is the number of nanoseconds in one second.
const NanosPerSecond int64 = 1_000_000_000

// Rational is an exact num/den ratio. Den is always positive.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NewRational normalises the sign of den and rejects a zero denominator.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrZeroDen
	}
	if den < 0 {
		if num == math.MinInt64 || den == math.MinInt64 {
			return Rational{}, ErrOverflow
		}
		num, den = -num, -den
	}
	return Rational{Num: num, Den: den}, nil
}

// Mul returns numerator*x/den as an exact quotient and remainder.
func (r Rational) Mul(x int64) (int64, int64, error) {
	return mulDiv(x, r.Num, r.Den)
}

// String renders the ratio for logs and JSON diagnostics.
func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// Tick is an exact media position: Value + Rem/RemDen ticks. Rem is kept in
// canonical form (0 <= Rem < RemDen) so equal quantities compare equal.
// A tick converted from event nanoseconds has RemDen = NanosPerSecond.
type Tick struct {
	Value  int64 `json:"value"`
	Rem    int64 `json:"rem"`
	RemDen int64 `json:"rem_den"`
}

// NewTick builds a canonical tick, borrowing whole units out of the remainder.
func NewTick(value, rem, den int64) (Tick, error) {
	if den <= 0 {
		return Tick{}, ErrZeroDen
	}
	// Fold an oversized remainder, including a negative one, into Value.
	if rem >= den || rem < 0 {
		q, r, err := mulDiv(rem, 1, den)
		if err != nil {
			return Tick{}, err
		}
		if r < 0 {
			q--
			r += den
		}
		sum, ok := addInt64(value, q)
		if !ok {
			return Tick{}, ErrOverflow
		}
		value, rem = sum, r
	}
	return Tick{Value: value, Rem: rem, RemDen: den}, nil
}

// Compare orders two ticks by exact rational value.
func (t Tick) Compare(o Tick) int {
	if t.Value != o.Value {
		if t.Value < o.Value {
			return -1
		}
		return 1
	}
	// Both Values equal: compare Rem/RemDen exactly via cross products.
	a, b := t.Rem, o.Rem
	ad, bd := t.RemDen, o.RemDen
	if ad == bd {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}
	// Cross-multiply with overflow detection; fall back to comparing the
	// fractional parts as exact quotients when the products are too large.
	l, okL := mulInt64(a, bd)
	r, okR := mulInt64(b, ad)
	if okL && okR {
		switch {
		case l < r:
			return -1
		case l > r:
			return 1
		default:
			return 0
		}
	}
	// Safe fallback: both fractions are in [0,1), compare via scaled digits.
	av, _, _ := mulDiv(a, 1_000_000_000, ad)
	bv, _, _ := mulDiv(b, 1_000_000_000, bd)
	switch {
	case av < bv:
		return -1
	case av > bv:
		return 1
	default:
		return 0
	}
}

// Stamp pairs the original nanosecond timestamp with its exact tick reading.
type Stamp struct {
	NS   int64 `json:"ns"`
	Tick Tick  `json:"tick"`
}

// Clock converts between event nanoseconds and one layer's media timescale.
type Clock struct {
	Timescale  uint32 `json:"timescale"`
	FrameTicks int64  `json:"frame_ticks"`
}

// New creates a clock. A per-layer frame duration is required so that the
// one-frame boundary tolerance is expressed in the same ticks.
func New(timescale uint32, frameTicks int64) (*Clock, error) {
	if timescale == 0 {
		return nil, ErrZeroTimescale
	}
	if frameTicks <= 0 {
		return nil, ErrZeroFrame
	}
	return &Clock{Timescale: timescale, FrameTicks: frameTicks}, nil
}

// FromNS converts event nanoseconds to ticks, preserving the remainder.
func (c *Clock) FromNS(ns int64) (Tick, error) {
	if c == nil || c.Timescale == 0 {
		return Tick{}, ErrZeroTimescale
	}
	q, r, err := mulDiv(ns, int64(c.Timescale), NanosPerSecond)
	if err != nil {
		return Tick{}, err
	}
	if r < 0 {
		q--
		r += NanosPerSecond
	}
	return Tick{Value: q, Rem: r, RemDen: NanosPerSecond}, nil
}

// ToNS converts a tick reading back to nanoseconds exactly. The second
// result is the leftover fraction of a nanosecond (0 <= rem < timescale)
// when the tick does not land on a whole nanosecond.
func (c *Clock) ToNS(t Tick) (int64, int64, error) {
	if c == nil || c.Timescale == 0 {
		return 0, 0, ErrZeroTimescale
	}
	if t.RemDen <= 0 {
		return 0, 0, ErrZeroDen
	}
	// Exact quantity in ns is N*1e9/(RemDen*Timescale) where
	// N = Value*RemDen + Rem. Compute the signed numerator with 128-bit
	// headroom, then floor-divide its magnitude.
	prod, ok := mulInt64(t.Value, t.RemDen)
	if !ok {
		return 0, 0, ErrOverflow
	}
	num, ok := addInt64(prod, t.Rem)
	if !ok {
		return 0, 0, ErrOverflow
	}
	neg := num < 0
	un, ok := abs64(num)
	if !ok {
		return 0, 0, ErrOverflow
	}
	denHi, denLo := bits.Mul64(uint64(t.RemDen), uint64(c.Timescale))
	if denHi != 0 {
		return 0, 0, ErrOverflow
	}
	den := denLo
	hi, lo := bits.Mul64(un, uint64(NanosPerSecond))
	if hi >= den {
		return 0, 0, ErrOverflow
	}
	q, r := bits.Div64(hi, lo, den)
	if q > math.MaxInt64 {
		return 0, 0, ErrOverflow
	}
	ns, rem := int64(q), int64(r)
	if neg {
		// Floor toward -infinity so the remainder stays non-negative.
		if rem != 0 {
			ns++
			rem = int64(den) - rem
		}
		ns = -ns
	}
	return ns, rem, nil
}

// StampNS converts an event timestamp and keeps the exact remainder.
func (c *Clock) StampNS(ns int64) (Stamp, error) {
	t, err := c.FromNS(ns)
	if err != nil {
		return Stamp{}, err
	}
	return Stamp{NS: ns, Tick: t}, nil
}

// AlignDown snaps a tick down to the layer frame grid, keeping its fraction.
func (c *Clock) AlignDown(t Tick) Tick {
	if c == nil || c.FrameTicks <= 0 || t.Value < 0 {
		return t
	}
	t.Value -= t.Value % c.FrameTicks
	return t
}

// AlignUp snaps a tick up to the layer frame grid, keeping its fraction.
func (c *Clock) AlignUp(t Tick) Tick {
	if c == nil || c.FrameTicks <= 0 || t.Value < 0 {
		return t
	}
	if d := t.Value % c.FrameTicks; d != 0 {
		t.Value += c.FrameTicks - d
	}
	return t
}

// SkewTicks is |a-b| in whole ticks, ignoring sub-tick fractions.
func (c *Clock) SkewTicks(a, b Tick) int64 {
	d := a.Value - b.Value
	if d < 0 {
		return -d
	}
	return d
}

// ExceedsFrame reports whether the skew is larger than one frame.
func (c *Clock) ExceedsFrame(a, b Tick) bool {
	return c.SkewTicks(a, b) > c.FrameTicks
}

// FrameNS is the layer frame duration in event nanoseconds (floored).
func (c *Clock) FrameNS() int64 {
	if c == nil {
		return 0
	}
	return c.FrameTicks * NanosPerSecond / int64(c.Timescale)
}

// FrameRatio returns the frame duration as an exact ratio.
func (c *Clock) FrameRatio() (Rational, error) {
	return NewRational(c.FrameTicks, int64(c.Timescale))
}

func addInt64(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a == -1 && b == math.MinInt64 || b == -1 && a == math.MinInt64 {
		return 0, false
	}
	p := a * b
	if p/b != a {
		return 0, false
	}
	return p, true
}

// mulDiv computes a*b/c exactly with an overflow check, returning quotient
// and remainder. Signs are handled without converting MinInt64 to positive.
func mulDiv(a, b, c int64) (int64, int64, error) {
	if c == 0 {
		return 0, 0, ErrZeroTimescale
	}
	neg := ((a < 0) != (b < 0)) != (c < 0)
	ua, ok := abs64(a)
	if !ok {
		return 0, 0, ErrOverflow
	}
	ub, ok := abs64(b)
	if !ok {
		return 0, 0, ErrOverflow
	}
	uc, ok := abs64(c)
	if !ok {
		return 0, 0, ErrOverflow
	}
	hi, lo := bits.Mul64(ua, ub)
	if hi >= uc {
		return 0, 0, ErrOverflow
	}
	q, r := bits.Div64(hi, lo, uc)
	if q > math.MaxInt64 {
		return 0, 0, ErrOverflow
	}
	qi, ri := int64(q), int64(r)
	if neg {
		qi = -qi
		ri = -ri
	}
	return qi, ri, nil
}

func abs64(v int64) (uint64, bool) {
	if v == math.MinInt64 {
		return 0, false
	}
	if v < 0 {
		return uint64(-v), true
	}
	return uint64(v), true
}
