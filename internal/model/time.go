// Package model defines the shared rational-time and error model used by
// every mediarc subsystem. All timestamps are integer ticks on an explicit
// timescale; conversions are exact integer arithmetic (no floats).
package model

import (
	"fmt"
	"math/big"
)

// Code is a stable machine-readable error code for reports.
type Code string

const (
	ErrTruncated    Code = "TRUNCATED"     // stream ends mid access-unit / OBU
	ErrOutOfRange   Code = "OUT_OF_RANGE"  // index record outside stream bounds
	ErrMisaligned   Code = "MISALIGNED"    // index offset not on an AU boundary
	ErrOverflow     Code = "OVERFLOW"      // integer overflow in length/time math
	ErrConflict     Code = "CONFLICT"      // same mapping position, different content
	ErrWrap         Code = "CLOCK_WRAP"    // 32-bit timestamp wraparound repaired
	ErrSourceSwitch Code = "SOURCE_SWITCH" // backward jump from a source switch
	ErrArchive      Code = "ARCHIVE"       // archive unrecoverable
	ErrInput        Code = "INPUT"         // unreadable input / bad arguments
	ErrPath         Code = "PATH"          // path escapes its base directory
)

// Error carries a stable code, the track and the first byte offset involved.
type Error struct {
	Code   Code   `json:"code"`
	Track  int    `json:"track"`
	Offset int64  `json:"offset"`
	Msg    string `json:"msg"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s track=%d offset=%d: %s", e.Code, e.Track, e.Offset, e.Msg)
}

// Time is a rational timestamp: Ticks ticks of 1/Scale second.
type Time struct {
	Ticks int64
	Scale int64 // ticks per second, must be > 0
}

// MulDiv returns v*mul/div truncated toward zero, or ErrOverflow if the
// exact result does not fit in an int64. Exact integer math, no floats.
func MulDiv(v, mul, div int64) (int64, *Error) {
	if div <= 0 {
		return 0, &Error{Code: ErrOverflow, Offset: -1, Msg: fmt.Sprintf("invalid divisor %d", div)}
	}
	r := new(big.Int).Mul(big.NewInt(v), big.NewInt(mul))
	r.Quo(r, big.NewInt(div))
	if !r.IsInt64() {
		return 0, &Error{Code: ErrOverflow, Offset: -1, Msg: fmt.Sprintf("muldiv(%d,%d,%d) overflows int64", v, mul, div)}
	}
	return r.Int64(), nil
}

// Convert re-expresses t on the given timescale using exact integer math.
func (t Time) Convert(scale int64) (Time, *Error) {
	if t.Scale <= 0 {
		return Time{}, &Error{Code: ErrOverflow, Offset: -1, Msg: fmt.Sprintf("invalid timescale %d", t.Scale)}
	}
	ticks, err := MulDiv(t.Ticks, scale, t.Scale)
	if err != nil {
		return Time{}, err
	}
	return Time{Ticks: ticks, Scale: scale}, nil
}

// Cmp compares two rational times exactly: -1, 0 or +1.
func (t Time) Cmp(o Time) int {
	l := new(big.Int).Mul(big.NewInt(t.Ticks), big.NewInt(o.Scale))
	r := new(big.Int).Mul(big.NewInt(o.Ticks), big.NewInt(t.Scale))
	return l.Cmp(r)
}
