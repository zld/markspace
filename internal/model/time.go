// Package model defines the shared primitives used by every subsystem:
// exact rational media time and the structured error type that flows
// through the pipeline unchanged.
package model

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Sentinel errors returned by time conversions. They are wrapped by callers
// in a *Error so that the failing asset/stage is always identifiable.
var (
	ErrZeroDenominator = errors.New("model: zero denominator")
	ErrZeroTimescale   = errors.New("model: zero timescale")
	ErrOverflow        = errors.New("model: arithmetic overflow")
)

// NanosPerSecond is the fixed conversion factor between seconds and the
// pipeline's canonical nanosecond unit. All conversions are integer only.
const NanosPerSecond int64 = 1_000_000_000

// Rational is an exact media rate num/den (den > 0). It never stores a float,
// so repeated conversions cannot accumulate rounding drift.
type Rational struct {
	Num int64
	Den int64
}

// NewRational validates and normalises a rational rate.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, ErrZeroDenominator
	}
	r := Rational{Num: num, Den: den}
	return r.normalized()
}

func (r Rational) normalized() (Rational, error) {
	if r.Den == 0 {
		return Rational{}, ErrZeroDenominator
	}
	if r.Den < 0 {
		if r.Den == math.MinInt64 || r.Num == math.MinInt64 {
			return Rational{}, ErrOverflow
		}
		r.Num = -r.Num
		r.Den = -r.Den
	}
	if r.Num == 0 {
		return Rational{Num: 0, Den: 1}, nil
	}
	g := gcd(abs64(r.Num), uint64(r.Den))
	return Rational{Num: r.Num / int64(g), Den: r.Den / int64(g)}, nil
}

// Add returns r+s exactly, or ErrOverflow.
func (r Rational) Add(s Rational) (Rational, error) {
	if r.Den == 0 || s.Den == 0 {
		return Rational{}, ErrZeroDenominator
	}
	g := gcd(uint64(abs64(r.Den)), uint64(abs64(s.Den)))
	rd := r.Den / int64(g)
	sd := s.Den / int64(g)
	a, err := mulInt64(r.Num, sd)
	if err != nil {
		return Rational{}, err
	}
	b, err := mulInt64(s.Num, rd)
	if err != nil {
		return Rational{}, err
	}
	num, err := addInt64(a, b)
	if err != nil {
		return Rational{}, err
	}
	den, err := mulInt64(rd, s.Den)
	if err != nil {
		return Rational{}, err
	}
	return NewRational(num, den)
}

// Sub returns r-s exactly.
func (r Rational) Sub(s Rational) (Rational, error) {
	if s.Num == math.MinInt64 {
		return Rational{}, ErrOverflow
	}
	return r.Add(Rational{Num: -s.Num, Den: s.Den})
}

// Cmp compares r and s without converting to floating point.
func (r Rational) Cmp(s Rational) int {
	if r.Den == 0 || s.Den == 0 {
		return 0
	}
	an, ad := abs64(r.Num), uint64(abs64(r.Den))
	bn, bd := abs64(s.Num), uint64(abs64(s.Den))
	hi1, lo1 := bits.Mul64(an, bd)
	hi2, lo2 := bits.Mul64(bn, ad)
	c := cmp128(hi1, lo1, hi2, lo2)
	negR := r.Num < 0
	negS := s.Num < 0
	switch {
	case negR && !negS:
		return -1
	case !negR && negS:
		return 1
	case c == 0:
		return 0
	case negR && negS:
		return -c
	default:
		return c
	}
}

// Nanos converts the rational number of seconds to nanoseconds using
// round-half-away-from-zero. It returns ErrOverflow when the result does not
// fit in an int64.
func (r Rational) Nanos() (Nanos, error) {
	v, err := mulDivRound(r.Num, NanosPerSecond, r.Den)
	return Nanos(v), err
}

// Float64 exists only for presentation. Callers must never accumulate floats.
func (r Rational) Float64() float64 {
	return float64(r.Num) / float64(r.Den)
}

func (r Rational) String() string {
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// Nanos is the canonical exact time unit of the pipeline.
type Nanos int64

// Milliseconds converts to whole milliseconds, rounding half away from zero.
func (n Nanos) Milliseconds() int64 {
	return (int64(n) + 500_000) / 1_000_000
}

// Microseconds returns exact microseconds rounded half away from zero.
func (n Nanos) Microseconds() int64 {
	return (int64(n) + 500) / 1_000
}

func (n Nanos) String() string {
	ms := int64(n) / 1_000_000
	us := (int64(n) % 1_000_000) / 1_000
	return fmt.Sprintf("%d.%03dms", ms, abs64(us)%1000)
}

// Time is a raw media timestamp that keeps every value needed to reproduce it
// later: the tick count, the track timescale and the edit-list media offset
// (in media ticks).
type Time struct {
	Ticks      int64
	Timescale  uint32
	EditOffset int64
}

// NewTime validates a raw timestamp.
func NewTime(ticks int64, timescale uint32, editOffset int64) (Time, error) {
	if timescale == 0 {
		return Time{}, ErrZeroTimescale
	}
	return Time{Ticks: ticks, Timescale: timescale, EditOffset: editOffset}, nil
}

// Rate returns the exact ticks-per-second rate of the timestamp.
func (t Time) Rate() Rational {
	return Rational{Num: 1, Den: int64(t.Timescale)}
}

// Nanos converts the raw tick count to nanoseconds, ignoring the edit offset.
func (t Time) Nanos() (Nanos, error) {
	if t.Timescale == 0 {
		return 0, ErrZeroTimescale
	}
	v, err := mulDivRound(t.Ticks, NanosPerSecond, int64(t.Timescale))
	return Nanos(v), err
}

// ShiftedNanos applies the edit-list media offset before converting to
// nanoseconds. This is how a media timestamp becomes a timeline timestamp.
func (t Time) ShiftedNanos() (Nanos, error) {
	if t.Timescale == 0 {
		return 0, ErrZeroTimescale
	}
	shifted, err := subInt64(t.Ticks, t.EditOffset)
	if err != nil {
		return 0, err
	}
	v, err := mulDivRound(shifted, NanosPerSecond, int64(t.Timescale))
	return Nanos(v), err
}

// WithEditOffset returns a copy carrying a different edit-list offset.
func (t Time) WithEditOffset(off int64) Time {
	t.EditOffset = off
	return t
}

// Sub returns the exact difference t-u in nanoseconds, requiring equal
// timescales so no hidden rounding is introduced.
func (t Time) Sub(u Time) (Nanos, error) {
	if t.Timescale == 0 || u.Timescale == 0 {
		return 0, ErrZeroTimescale
	}
	if t.Timescale != u.Timescale {
		return 0, fmt.Errorf("%w: timescale mismatch %d vs %d", ErrOverflow, t.Timescale, u.Timescale)
	}
	d, err := subInt64(t.Ticks, u.Ticks)
	if err != nil {
		return 0, err
	}
	v, err := mulDivRound(d, NanosPerSecond, int64(t.Timescale))
	return Nanos(v), err
}

// Span is a half-open [Start, End) interval on the common timeline.
type Span struct {
	Start Nanos
	End   Nanos
}

// NewSpan validates ordering.
func NewSpan(start, end Nanos) (Span, error) {
	if end < start {
		return Span{}, fmt.Errorf("model: span end %d before start %d", end, start)
	}
	return Span{Start: start, End: end}, nil
}

func (s Span) Duration() Nanos { return s.End - s.Start }

// Contains reports whether n lies inside the half-open span.
func (s Span) Contains(n Nanos) bool { return n >= s.Start && n < s.End }

// Overlaps reports whether two spans share any time.
func (s Span) Overlaps(o Span) bool { return s.Start < o.End && o.Start < s.End }

// Intersect returns the shared sub-span.
func (s Span) Intersect(o Span) (Span, bool) {
	start := s.Start
	if o.Start > start {
		start = o.Start
	}
	end := s.End
	if o.End < end {
		end = o.End
	}
	if end <= start {
		return Span{}, false
	}
	return Span{Start: start, End: end}, true
}

func (s Span) String() string {
	return fmt.Sprintf("[%s,%s)", s.Start, s.End)
}

// NanosFromRatio converts n/d (d>0) seconds to nanoseconds exactly.
func NanosFromRatio(n, d int64) (Nanos, error) {
	v, err := mulDivRound(n, NanosPerSecond, d)
	return Nanos(v), err
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(x int64) uint64 {
	if x < 0 {
		return uint64(-(x + 1)) + 1
	}
	return uint64(x)
}

func mulInt64(a, b int64) (int64, error) {
	hi, lo := bits.Mul64(abs64(a), abs64(b))
	q, err := toInt64(hi, lo)
	if err != nil {
		return 0, err
	}
	if (a < 0) != (b < 0) {
		return -q, nil
	}
	return q, nil
}

func addInt64(a, b int64) (int64, error) {
	sum := a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func subInt64(a, b int64) (int64, error) {
	if b == math.MinInt64 {
		return 0, ErrOverflow
	}
	return addInt64(a, -b)
}

func toInt64(hi, lo uint64) (int64, error) {
	if hi != 0 || lo > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return int64(lo), nil
}

func cmp128(hi1, lo1, hi2, lo2 uint64) int {
	if hi1 != hi2 {
		if hi1 < hi2 {
			return -1
		}
		return 1
	}
	if lo1 != lo2 {
		if lo1 < lo2 {
			return -1
		}
		return 1
	}
	return 0
}

// mulDivRound returns round-half-away-from-zero of (a*b)/c using 128-bit
// intermediates. c must be non-zero.
func mulDivRound(a, b, c int64) (int64, error) {
	if c == 0 {
		return 0, ErrZeroDenominator
	}
	ua, ub, uc := abs64(a), abs64(b), abs64(c)
	neg := (a < 0) != (b < 0)
	neg = neg != (c < 0)
	if ua == 0 || ub == 0 {
		return 0, nil
	}
	hi, lo := bits.Mul64(ua, ub)
	if hi >= uc {
		return 0, ErrOverflow
	}
	q, r := bits.Div64(hi, lo, uc)
	// round half away from zero: if 2*r >= uc, increment
	if r >= uc-r {
		if q == math.MaxUint64 {
			return 0, ErrOverflow
		}
		q++
	}
	if neg {
		if q > uint64(math.MaxInt64)+1 {
			return 0, ErrOverflow
		}
		if q == uint64(math.MaxInt64)+1 {
			return math.MinInt64, nil
		}
		return -int64(q), nil
	}
	if q > uint64(math.MaxInt64) {
		return 0, ErrOverflow
	}
	return int64(q), nil
}
