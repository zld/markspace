// Package model defines the rational media-time and error-record models shared
// by every subsystem: scan, timeline, cache, recover, manifest and verify.
package model

import (
	"errors"
	"math/big"
)

// Rational is an exact media time base. All timestamp math keeps the original
// Num/Den pair instead of floating-point seconds, so no precision is lost when
// tracks use different time bases (e.g. 90 kHz RTP vs. 1000-unit MP4).
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// nanosecondBase is the canonical time base used for cross-track alignment.
const nanosecondBase int64 = 1_000_000_000

// ErrOverflow is returned whenever a timestamp conversion does not fit in the
// target representation. Callers must surface it rather than wrapping around.
var ErrOverflow = errors.New("model: timestamp overflow")

// Validate rejects zero/negative denominators and zero numerators are allowed.
func (r Rational) Validate() error {
	if r.Den <= 0 || r.Num < 0 {
		return errors.New("model: invalid rational time base")
	}
	return nil
}

// mul64 returns a*b with an exact overflow flag (math/big keeps it correct on
// both 32- and 64-bit targets).
func mul64(a, b int64) (int64, bool) {
	p := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	return p.Int64(), p.IsInt64()
}

// ToNanos converts a tick count in this rational base to canonical nanoseconds.
// It returns ErrOverflow instead of silently wrapping when the result does not
// fit into an int64 nanosecond value.
func (r Rational) ToNanos(ticks int64) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	if ticks < 0 {
		return 0, ErrOverflow
	}
	num, ok := mul64(ticks, nanosecondBase)
	if !ok {
		return 0, ErrOverflow
	}
	// Divide first to keep the intermediate as small as possible.
	whole, rem := num/r.Den, num%r.Den
	if whole < 0 || (whole == (1<<63-1) && rem != 0) {
		return 0, ErrOverflow
	}
	return whole, nil
}

// ToTicks converts canonical nanoseconds back into a tick count in this base,
// returning ErrOverflow on out-of-range input.
func (r Rational) ToTicks(nanos int64) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	if nanos < 0 {
		return 0, ErrOverflow
	}
	num, ok := mul64(nanos, r.Den)
	if !ok {
		return 0, ErrOverflow
	}
	return num / nanosecondBase, nil
}

// CompareNanos compares two (ticks, base) pairs exactly using cross products.
// Returns -1, 0 or 1. Overflow in the cross product is checked explicitly.
func CompareNanos(t1 int64, b1 Rational, t2 int64, b2 Rational) (int, error) {
	if err := b1.Validate(); err != nil {
		return 0, err
	}
	if err := b2.Validate(); err != nil {
		return 0, err
	}
	l, ok1 := mul64(t1, b2.Den)
	r, ok2 := mul64(t2, b1.Den)
	if !ok1 || !ok2 {
		// Fall back to big.Int comparison for the rare overflow case.
		bl := new(big.Int).Mul(big.NewInt(t1), big.NewInt(b2.Den))
		br := new(big.Int).Mul(big.NewInt(t2), big.NewInt(b1.Den))
		return bl.Cmp(br), nil
	}
	switch {
	case l < r:
		return -1, nil
	case l > r:
		return 1, nil
	default:
		return 0, nil
	}
}

// NanosToSeconds renders canonical nanoseconds as seconds for terminal output.
func NanosToSeconds(nanos int64) float64 {
	return float64(nanos) / float64(nanosecondBase)
}

// SecondsToNanos converts a seconds figure used by the CLI (e.g. "1s") to the
// canonical representation, rejecting negative input.
func SecondsToNanos(secs float64) (int64, error) {
	if secs < 0 {
		return 0, ErrOverflow
	}
	v := secs * float64(nanosecondBase)
	if v >= float64(1<<63-1) {
		return 0, ErrOverflow
	}
	return int64(v), nil
}
