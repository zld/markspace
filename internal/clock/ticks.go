// Package clock provides the shared nanosecond time scale and rational
// time-base conversions used by every subsystem. All durations that cross
// subsystem boundaries are expressed as Ticks (int64 nanoseconds).
package clock

import (
	"math"
	"math/big"

	"mediaverify/internal/errs"
)

// Ticks is a point in time or a duration on the shared nanosecond scale.
type Ticks int64

// Nanoseconds builds Ticks from a raw nanosecond count.
func Nanoseconds(ns int64) Ticks { return Ticks(ns) }

// Ns returns the raw nanosecond count.
func (t Ticks) Ns() int64 { return int64(t) }

// Rational expresses a time base as numerator/denominator (seconds).
// For example 90kHz RTP time is Rational{1, 90000}, 48kHz PCM is {1,48000}.
type Rational struct {
	Num int64
	Den int64
}

// Seconds is the rational value rendered as floating point (reporting only;
// deterministic code paths must use the integer/big-int conversions).
func (r Rational) Seconds() float64 {
	if r.Den == 0 {
		return 0
	}
	return float64(r.Num) / float64(r.Den)
}

// ScaleToTicks converts value units of the given rational time base onto the
// shared nanosecond scale: ticks = value * num/den * 1e9. The intermediary
// product goes through big.Int so 32-bit RTP timestamps and large frame
// counts never overflow int64 before the final division.
func ScaleToTicks(value int64, r Rational) (Ticks, error) {
	if r.Den <= 0 || r.Num <= 0 {
		return 0, errs.New(errs.KindInvalidInput, "clock", "illegal rational %d/%d", r.Num, r.Den)
	}
	num := new(big.Int).Mul(big.NewInt(value), big.NewInt(r.Num))
	num.Mul(num, big.NewInt(1_000_000_000))
	q := num.Quo(num, big.NewInt(r.Den))
	if !q.IsInt64() {
		return 0, errs.New(errs.KindTimestamp, "clock", "ticks overflow for value=%d", value)
	}
	return Ticks(q.Int64()), nil
}

// TicksToScale is the exact inverse of ScaleToTicks, rounded to nearest.
func TicksToScale(t Ticks, r Rational) (int64, error) {
	if r.Den <= 0 || r.Num <= 0 {
		return 0, errs.New(errs.KindInvalidInput, "clock", "illegal rational %d/%d", r.Num, r.Den)
	}
	num := new(big.Int).Mul(big.NewInt(int64(t)), big.NewInt(r.Den))
	den := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(1_000_000_000))
	q := new(big.Int).Quo(num, den)
	rem := new(big.Int).Rem(num, den)
	twoRem := new(big.Int).Lsh(rem, 1)
	if twoRem.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, errs.New(errs.KindTimestamp, "clock", "scale overflow for ticks=%d", t)
	}
	return q.Int64(), nil
}

// FrameDuration returns the exact per-frame duration for a frame rate.
func FrameDuration(r Rational) Ticks {
	t, err := ScaleToTicks(1, r)
	if err != nil {
		return 0
	}
	return t
}

// SnapToGrid rounds t to the nearest multiple of grid; ties round up.
func SnapToGrid(t, grid Ticks) Ticks {
	if grid <= 0 {
		return t
	}
	k := int64(t) / int64(grid)
	rem := int64(t) % int64(grid)
	if rem < 0 {
		k--
		rem += int64(grid)
	}
	if rem*2 >= int64(grid) {
		k++
	}
	return Ticks(k * int64(grid))
}

// Abs returns the absolute duration of d.
func Abs(d Ticks) Ticks {
	if d < 0 {
		return -d
	}
	return d
}

// Min/max helpers for Ticks.
func Min(a, b Ticks) Ticks {
	if a < b {
		return a
	}
	return b
}

// Max returns the larger of a and b.
func Max(a, b Ticks) Ticks {
	if a > b {
		return a
	}
	return b
}

// SecondsFloat renders ticks as fractional seconds (reporting only).
func (t Ticks) SecondsFloat() float64 {
	return float64(int64(t)) / 1e9
}

// ClampFloat constrains v to [lo, hi].
func ClampFloat(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
