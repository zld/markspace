// Package timebase provides integer rational time conversions.
//
// Timeline arithmetic never accumulates floating-point rounding: clip points,
// transition intervals, event ticks, and media sample counts are all converted
// with integer multiply/divide operations.
package timebase

import (
	"errors"
	"fmt"
	"math/big"
)

// Rates used by the supported fixture and delivery formats.
const (
	EventRate uint32 = 90_000
	VideoRate uint32 = 90_000
	AudioRate uint32 = 48_000
	VideoFPS        = 25
	FrameDuration   = 3600 // 90 kHz / 25 fps
)

// Rational is num/den seconds. Den must be positive.
type Rational struct {
	Num int64
	Den int64
}

// Ms returns an exact rational representation of milliseconds.
func Ms(ms int64) Rational { return Rational{Num: ms, Den: 1000} }

// Seconds returns an exact rational representation of seconds.
func Seconds(n int64) Rational { return Rational{Num: n, Den: 1} }

func (r Rational) validate() error {
	if r.Den <= 0 {
		return fmt.Errorf("timebase: non-positive denominator %d", r.Den)
	}
	return nil
}

// Floor rescales the rational to ticks at rate and rounds toward negative infinity.
func (r Rational) Floor(rate uint32) (int64, error) {
	if rate == 0 {
		return 0, errors.New("timebase: zero rate")
	}
	if err := r.validate(); err != nil {
		return 0, err
	}
	return MulDivFloor(r.Num, int64(rate), r.Den), nil
}

// Nearest rescales to the nearest integer tick, with halves rounded upward.
func (r Rational) Nearest(rate uint32) (int64, error) {
	if rate == 0 {
		return 0, errors.New("timebase: zero rate")
	}
	if err := r.validate(); err != nil {
		return 0, err
	}
	num := r.Num * int64(rate)
	if num >= 0 {
		return (num + r.Den/2) / r.Den, nil
	}
	// Negative times are not used by clips, but keep the function generally correct.
	return -((-num + r.Den/2) / r.Den), nil
}

// MulDivFloor computes floor(a*b/d) without float arithmetic.
func MulDivFloor(a, b, d int64) int64 {
	if d <= 0 {
		panic("timebase: MulDivFloor requires positive denominator")
	}
	num := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(num, big.NewInt(d), rem)
	// Quo truncates toward zero; adjust negative, non-divisible results to floor.
	if rem.Sign() != 0 && num.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	return q.Int64()
}

// Scale converts a tick count between integer clock rates, rounding down.
func Scale(tick int64, from, to uint32) int64 {
	if from == 0 || to == 0 {
		panic("timebase: zero clock rate")
	}
	return MulDivFloor(tick, int64(to), int64(from))
}

// Range is a half-open tick interval [Start, End) in one time base.
type Range struct {
	Start int64
	End   int64
}

// NewRange validates and constructs a half-open range.
func NewRange(start, end int64) (Range, error) {
	if end < start {
		return Range{}, fmt.Errorf("timebase: inverted range [%d,%d)", start, end)
	}
	return Range{Start: start, End: end}, nil
}

// Len returns the number of ticks in the half-open interval.
func (r Range) Len() int64 { return r.End - r.Start }

// Contains reports whether tick is in [Start, End).
func (r Range) Contains(tick int64) bool {
	return tick >= r.Start && tick < r.End
}

// Intersect returns the half-open intersection of two ranges.
func Intersect(a, b Range) (Range, bool) {
	start := max(a.Start, b.Start)
	end := min(a.End, b.End)
	if start >= end {
		return Range{}, false
	}
	return Range{Start: start, End: end}, true
}

// AlignError returns abs(want-got) expressed in milliseconds as an integer.
func AlignError(want, got int64, rate uint32) int64 {
	d := want - got
	if d < 0 {
		d = -d
	}
	return Scale(d, rate, 1000)
}
