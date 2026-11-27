// Package model defines the time basis and structured error model shared by
// every stage of the media asset pipeline. All time math is integer or exact
// rational arithmetic; floating point must never be used to accumulate time.
package model

import (
	"errors"
	"math"
	"math/bits"
)

// NanoPerSec is the single integer nanosecond tick used across the pipeline.
const NanoPerSec int64 = 1_000_000_000

// Rational is an exact non-negative fraction with a strictly positive
// denominator. It is used for frame rates and other rates that have no exact
// fixed-point representation (for example 30000/1001).
type Rational struct {
	Num int64
	Den int64
}

// Validate rejects zero or negative denominators and zero numerators that
// would produce a zero rate/time base.
func (r Rational) Validate(allowZeroNum bool) error {
	if r.Den <= 0 {
		return New(ErrUnrepresentableTime, "", -1, "rational denominator must be positive: %d", r.Den)
	}
	if r.Num < 0 {
		return New(ErrUnrepresentableTime, "", -1, "rational numerator must be non-negative: %d", r.Num)
	}
	if !allowZeroNum && r.Num == 0 {
		return New(ErrZeroTimebase, "", -1, "rational rate is zero")
	}
	return nil
}

// Cmp returns -1, 0 or 1 like bytes.Compare.
func (r Rational) Cmp(o Rational) int {
	// a/b vs c/d  <=>  a*d vs c*b.
	l, ok1 := CheckedMul(r.Num, o.Den)
	rr, ok2 := CheckedMul(o.Num, r.Den)
	if ok1 != nil || ok2 != nil {
		// Inputs in this project are bounded, so this path is defensive only.
		fl := float64(r.Num) / float64(r.Den)
		fr := float64(o.Num) / float64(o.Den)
		switch {
		case fl < fr:
			return -1
		case fl > fr:
			return 1
		default:
			return 0
		}
	}
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// CheckedMul multiplies two non-negative int64 values, returning an error on
// overflow instead of wrapping.
func CheckedMul(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, New(ErrUnrepresentableTime, "", -1, "negative operand in CheckedMul: %d * %d", a, b)
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	// Overflow when the product does not fit in a non-negative int64: either
	// high 64 bits are set or the sign bit of the low word is set.
	if hi != 0 || lo > math.MaxInt64 {
		return 0, New(ErrUnrepresentableTime, "", -1, "integer overflow: %d * %d", a, b)
	}
	return int64(lo), nil
}

// CheckedAdd adds two non-negative int64 values, returning an error on
// overflow.
func CheckedAdd(a, b int64) (int64, error) {
	s, carry := bits.Add64(uint64(a), uint64(b), 0)
	if carry != 0 || s > math.MaxInt64 {
		return 0, New(ErrUnrepresentableTime, "", -1, "integer overflow: %d + %d", a, b)
	}
	return int64(s), nil
}

// RoundDiv returns the rounded quotient a/b (round half away from zero) for
// b > 0. Both arguments are expected non-negative.
func RoundDiv(a, b int64) (int64, error) {
	if b <= 0 {
		return 0, New(ErrUnrepresentableTime, "", -1, "RoundDiv denominator must be positive: %d", b)
	}
	if a < 0 {
		return 0, New(ErrUnrepresentableTime, "", -1, "RoundDiv numerator must be non-negative: %d", a)
	}
	half := b / 2
	if a > math.MaxInt64-half {
		return 0, New(ErrUnrepresentableTime, "", -1, "integer overflow in RoundDiv: %d + %d", a, half)
	}
	return (a + half) / b, nil
}

// FramePTSNS returns the exact presentation timestamp of frame i in integer
// nanoseconds, rounded once. It never accumulates rounding across frames:
// every call is derived directly from the frame index.
func FramePTSNS(frame int64, fps Rational) (int64, error) {
	if err := fps.Validate(false); err != nil {
		return 0, err
	}
	if frame < 0 {
		return 0, New(ErrUnrepresentableTime, "", -1, "negative frame index: %d", frame)
	}
	num, err := CheckedMul(frame, NanoPerSec)
	if err != nil {
		return 0, err
	}
	num, err = CheckedMul(num, fps.Den)
	if err != nil {
		return 0, err
	}
	return RoundDiv(num, fps.Num)
}

// FramePTSTicks returns the presentation timestamp of frame i expressed in
// track ticks, rounded once from the exact rational value.
func FramePTSTicks(frame int64, timescale uint32, fps Rational) (int64, error) {
	if timescale == 0 {
		return 0, New(ErrZeroTimebase, "", -1, "track timescale must not be zero")
	}
	if err := fps.Validate(false); err != nil {
		return 0, err
	}
	if frame < 0 {
		return 0, New(ErrUnrepresentableTime, "", -1, "negative frame index: %d", frame)
	}
	num, err := CheckedMul(frame, int64(timescale))
	if err != nil {
		return 0, err
	}
	num, err = CheckedMul(num, fps.Den)
	if err != nil {
		return 0, err
	}
	return RoundDiv(num, fps.Num)
}

// TicksToNS converts a track tick offset to integer nanoseconds with a single
// rounding step. A zero timescale is a fatal time-base error.
func TicksToNS(ticks int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, New(ErrZeroTimebase, "", -1, "track timescale must not be zero")
	}
	if ticks < 0 {
		return 0, New(ErrUnrepresentableTime, "", -1, "negative tick offset: %d", ticks)
	}
	num, err := CheckedMul(ticks, NanoPerSec)
	if err != nil {
		return 0, err
	}
	return RoundDiv(num, int64(timescale))
}

// AlignmentErrorNS returns the absolute difference between the exact
// nanosecond PTS of a frame and the nanosecond value implied by its rounded
// ticks. It is computed entirely with integer math.
func AlignmentErrorNS(frame int64, timescale uint32, fps Rational) (int64, error) {
	exactNS, err := FramePTSNS(frame, fps)
	if err != nil {
		return 0, err
	}
	ticks, err := FramePTSTicks(frame, timescale, fps)
	if err != nil {
		return 0, err
	}
	tickNS, err := TicksToNS(ticks, timescale)
	if err != nil {
		return 0, err
	}
	if exactNS > tickNS {
		return exactNS - tickNS, nil
	}
	return tickNS - exactNS, nil
}

// AsStructured unwraps err to a *Error if possible.
func AsStructured(err error) (*Error, bool) {
	var se *Error
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
