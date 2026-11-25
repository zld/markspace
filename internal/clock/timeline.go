// Package clock defines the project-wide media time base and checked rational conversions.
package clock

import (
	"fmt"
	"math/big"

	"mediacore/internal/diagnostic"
)

// UnifiedTimescale is nanosecond-like ticks per second. It is deliberately fine
// enough that all fixtures map exactly while keeping int64 range practical.
const UnifiedTimescale int64 = 1_000_000_000

// AlignmentLimitTicks is 20 ms in the unified time base.
const AlignmentLimitTicks int64 = 20_000_000

// Rational is num/den seconds. Denominator is always positive after construction.
type Rational struct {
	Num int64
	Den int64
}

func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, diagnostic.Wrap(diagnostic.ErrZeroDenominator, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "rational denominator is zero")
	}
	if den < 0 {
		num = -num
		den = -den
	}
	return Rational{Num: num, Den: den}, nil
}

func FromTicks(ticks, timescale int64) (Rational, error) {
	if timescale == 0 {
		return Rational{}, diagnostic.Wrap(diagnostic.ErrZeroTimescale, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "timescale is zero")
	}
	return NewRational(ticks, timescale)
}

func bigInt64(v int64) *big.Int { return new(big.Int).SetInt64(v) }

// ConvertTicks converts v in fromScale ticks/second to the nearest toScale tick.
// All arithmetic before rounding is exact integer arithmetic.
func ConvertTicks(v, fromScale, toScale int64) (int64, error) {
	if fromScale == 0 || toScale == 0 {
		return 0, diagnostic.Wrap(diagnostic.ErrZeroDenominator, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "timescale is zero")
	}
	if fromScale < 0 || toScale < 0 {
		return 0, fmt.Errorf("%w: negative timescale", diagnostic.ErrOverflow)
	}
	num := new(big.Int).Mul(bigInt64(v), bigInt64(toScale))
	den := bigInt64(fromScale)
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twiceRem := new(big.Int).Abs(rem)
	twiceRem.Mul(twiceRem, big.NewInt(2))
	absDen := new(big.Int).Abs(den)
	if twiceRem.Cmp(absDen) >= 0 {
		if num.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "time conversion overflows int64")
	}
	return q.Int64(), nil
}

// ConvertExact converts only when the result is an integral target tick.
func ConvertExact(v, fromScale, toScale int64) (int64, error) {
	if fromScale == 0 || toScale == 0 {
		return 0, diagnostic.Wrap(diagnostic.ErrZeroDenominator, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "timescale is zero")
	}
	num := new(big.Int).Mul(bigInt64(v), bigInt64(toScale))
	den := bigInt64(fromScale)
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 {
		return 0, fmt.Errorf("non-integral exact conversion: %d/%d to scale %d", v, fromScale, toScale)
	}
	if !q.IsInt64() {
		return 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "exact conversion overflows int64")
	}
	return q.Int64(), nil
}

// ToUnified maps source timescale ticks to unified ticks using nearest rounding.
func ToUnified(v, timescale int64) (int64, error) {
	return ConvertTicks(v, timescale, UnifiedTimescale)
}

// DurationToUnified maps a duration. It is an alias to make call sites self-documenting.
func DurationToUnified(d, timescale int64) (int64, error) {
	return ToUnified(d, timescale)
}

// Add returns a+b with int64 overflow detection.
func Add(a, b int64) (int64, error) {
	s := new(big.Int).Add(bigInt64(a), bigInt64(b))
	if !s.IsInt64() {
		return 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "tick addition overflows int64")
	}
	return s.Int64(), nil
}

// Diff returns a-b in the unified tick scale with overflow checking.
func Diff(a, b int64) (int64, error) {
	ba := bigInt64(a)
	br := bigInt64(b)
	d := ba.Sub(ba, br)
	if !d.IsInt64() {
		return 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "tick difference overflows int64")
	}
	return d.Int64(), nil
}

// AbsDiff returns |a-b| in unified ticks.
func AbsDiff(a, b int64) (int64, error) {
	d, err := Diff(a, b)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		if d == -1<<63 {
			return 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "absolute tick difference overflows")
		}
		d = -d
	}
	return d, nil
}

// AlignmentWithinLimit reports whether |start-reference| is at most 20 ms.
func AlignmentWithinLimit(startU, referenceU int64) (bool, int64, error) {
	d, err := AbsDiff(startU, referenceU)
	if err != nil {
		return false, 0, err
	}
	return d <= AlignmentLimitTicks, d, nil
}

// RationalTicks converts num/den seconds to scale ticks/second, nearest rounding.
func RationalTicks(r Rational, scale int64) (int64, error) {
	if scale == 0 {
		return 0, diagnostic.Wrap(diagnostic.ErrZeroDenominator, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "target timescale is zero")
	}
	num := new(big.Int).Mul(bigInt64(r.Num), bigInt64(scale))
	den := bigInt64(r.Den)
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Abs(rem)
	twice.Mul(twice, big.NewInt(2))
	if twice.Cmp(new(big.Int).Abs(den)) >= 0 {
		if num.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "rational tick conversion overflows int64")
	}
	return q.Int64(), nil
}

// Timeline is a monotonically advancing continuous program clock.
type Timeline struct {
	CurrentU int64
}

func NewTimeline() *Timeline { return &Timeline{} }

// Advance appends duration expressed in timescale and returns [start,end) in unified ticks.
// Discontinuities intentionally do not reset CurrentU.
func (t *Timeline) Advance(duration, timescale int64) (startU, endU int64, err error) {
	if duration < 0 {
		return 0, 0, diagnostic.Wrap(diagnostic.ErrTimeRegression, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "negative duration")
	}
	d, err := DurationToUnified(duration, timescale)
	if err != nil {
		return 0, 0, err
	}
	startU = t.CurrentU
	sum := new(big.Int).Add(bigInt64(startU), bigInt64(d))
	if !sum.IsInt64() {
		return 0, 0, diagnostic.Wrap(diagnostic.ErrOverflow, diagnostic.Location{Stage: "clock", ByteOffset: -1}, "timeline advance overflows")
	}
	endU = sum.Int64()
	t.CurrentU = endU
	return startU, endU, nil
}
