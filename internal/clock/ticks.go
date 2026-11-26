// Package clock defines the shared 1/90000 second timebase used by every
// subsystem. All arithmetic uses int64 ticks and rejects overflow instead
// of silently wrapping.
package clock

import (
	"math"

	"mediamerge/internal/fault"
)

// Resolution is the number of ticks per second.
const Resolution int64 = 90000

// Ticks is a non-negative point or duration on the shared timebase.
type Ticks int64

// MaxTicks is the largest representable tick value.
const MaxTicks = Ticks(math.MaxInt64)

var (
	errMulOverflow = fault.New(fault.StageClock, fault.KindOverflow, "", "tick multiplication overflow")
	errAddOverflow = fault.New(fault.StageClock, fault.KindOverflow, "", "tick addition overflow")
)

// mul64 returns a*b or an overflow fault.
func mul64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/b != a || r/a != b {
		return 0, errMulOverflow
	}
	return r, nil
}

// add64 returns a+b or an overflow fault.
func add64(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, errAddOverflow
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, errAddOverflow
	}
	return a + b, nil
}

// FromSeconds converts a whole-second duration to ticks.
func FromSeconds(sec int64) (Ticks, error) {
	r, err := mul64(sec, Resolution)
	return Ticks(r), err
}

// FromFraction converts num/den seconds to ticks, rounding down.
func FromFraction(num, den int64) (Ticks, error) {
	if den == 0 {
		return 0, fault.New(fault.StageClock, fault.KindOverflow, "", "zero denominator in fraction")
	}
	r, err := mul64(num, Resolution)
	if err != nil {
		return 0, err
	}
	return Ticks(r / den), nil
}

// Add returns t+d with overflow detection.
func (t Ticks) Add(d Ticks) (Ticks, error) {
	r, err := add64(int64(t), int64(d))
	return Ticks(r), err
}

// Sub returns t-d with underflow detection (negative results are rejected).
func (t Ticks) Sub(d Ticks) (Ticks, error) {
	r, err := add64(int64(t), -int64(d))
	if err != nil {
		return 0, err
	}
	if r < 0 {
		return 0, fault.New(fault.StageClock, fault.KindOverflow, "", "tick subtraction produced negative time")
	}
	return Ticks(r), nil
}

// Scale returns t*n/d (rational scaling), rounding down, with overflow checks.
func (t Ticks) Scale(n, d int64) (Ticks, error) {
	if d == 0 {
		return 0, fault.New(fault.StageClock, fault.KindOverflow, "", "zero denominator in Scale")
	}
	r, err := mul64(int64(t), n)
	if err != nil {
		return 0, err
	}
	return Ticks(r / d), nil
}

// CheckMonotonic rejects a non-advancing or backwards jump as a wrap fault.
// Callers that legitimately unwrap a 32-bit counter must do so before calling.
func CheckMonotonic(source string, prev, next Ticks) error {
	if next < prev {
		return fault.New(fault.StageClock, fault.KindWrap, source,
			"timestamp went backwards; wraparound is not silently corrected")
	}
	return nil
}
