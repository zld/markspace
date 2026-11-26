// Package timebase provides the rational sample-tick conversions and the
// single error model shared by the WAV, RTP and derived-fragment subsystems.
package timebase

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Code is a stable, machine-readable error identifier. Codes must not be
// renamed once fixtures or scripts depend on them.
type Code string

const (
	CodeInvalidArgument Code = "invalid_argument"
	CodeOverflow        Code = "overflow"
	CodeRIFF            Code = "riff_header"
	CodeFmt             Code = "fmt_chunk"
	CodeData            Code = "data_chunk"
	CodeAlignment       Code = "frame_alignment"
	CodeDigest          Code = "digest_mismatch"
	CodeReorder         Code = "rtp_reorder"
	CodeLate            Code = "rtp_late_window"
	CodeGap             Code = "gap"
	CodeBudget          Code = "budget_unavailable"
	CodeTrack           Code = "track_invalid"
	CodeFragment        Code = "fragment_invalid"
	CodeManifest        Code = "manifest_invalid"
	CodeCache           Code = "cache_invalid"
	CodeMP4             Code = "mp4_invalid"
	CodeExternalBad     Code = "external_bad_input"
)

// Error implements error so stable codes can be used directly with errors.Is.
func (c Code) Error() string { return string(c) }

// Interval is an optional half-open tick interval [Start, End). Negative
// values mean that the field is not known or not applicable.
type Interval struct {
	Start int64 `json:"start_tick"`
	End   int64 `json:"end_tick"`
}

// Error is the canonical error carried across package boundaries.
type Error struct {
	Code    Code     `json:"code"`
	Message string   `json:"message"`
	Offset  int64    `json:"raw_offset"`
	Track   string   `json:"track,omitempty"`
	Range   Interval `json:"time_range"`
	Cause   error    `json:"-"`
}

func NewError(code Code, message string) *Error {
	return &Error{Code: code, Message: message, Offset: -1, Range: Interval{Start: -1, End: -1}}
}

func At(code Code, offset int64, format string, args ...any) *Error {
	e := NewError(code, fmt.Sprintf(format, args...))
	e.Offset = offset
	return e
}

func (e *Error) WithTrack(track string) *Error {
	e.Track = track
	return e
}

func (e *Error) WithRange(start, end int64) *Error {
	e.Range = Interval{Start: start, End: end}
	return e
}

func (e *Error) Wrap(cause error) *Error {
	e.Cause = cause
	return e
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s", e.Code, e.Message)
	if e.Offset >= 0 {
		s += fmt.Sprintf(" at_byte=%d", e.Offset)
	}
	if e.Track != "" {
		s += " track=" + e.Track
	}
	if e.Range.Start >= 0 || e.Range.End >= 0 {
		s += fmt.Sprintf(" ticks=[%d,%d)", e.Range.Start, e.Range.End)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Cause }

// Is makes errors.Is(err, targetCode) work for both *Error and Code values.
func (e *Error) Is(target error) bool {
	if c, ok := target.(Code); ok {
		return e.Code == c
	}
	var te *Error
	if errors.As(target, &te) {
		return e.Code == te.Code
	}
	return false
}

// IsCode reports whether err contains the stable code c.
func IsCode(err error, c Code) bool {
	var te *Error
	return errors.As(err, &te) && te.Code == c
}

// AsError returns the canonical Error and true when err carries one.
func AsError(err error) (*Error, bool) {
	var te *Error
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

// Rational is a non-negative rational scale. It is used to make conversion
// assumptions explicit rather than hiding integer division at call sites.
type Rational struct {
	Num int64
	Den int64
}

// TickRate is the fixed manifest/fragment timebase: 1 tick = 1 nanosecond.
// Audio packet timing remains in rational sample units before conversion.
const TickRate uint32 = 1_000_000_000

// SamplesToTicks converts frame count at sampleRate to the common tick rate.
// Multiplication is checked before division.
func SamplesToTicks(samples int64, sampleRate, tickRate uint32) (int64, error) {
	if samples < 0 {
		return 0, NewError(CodeInvalidArgument, "negative sample count")
	}
	if sampleRate == 0 || tickRate == 0 {
		return 0, NewError(CodeInvalidArgument, "zero timebase denominator")
	}
	hi, lo := bits.Mul64(uint64(samples), uint64(tickRate))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, NewError(CodeOverflow, "sample-to-tick multiplication overflow")
	}
	return int64(lo) / int64(sampleRate), nil
}

// TicksToSamples converts ticks at tickRate to a frame count at sampleRate.
func TicksToSamples(ticks int64, tickRate, sampleRate uint32) (int64, error) {
	if ticks < 0 {
		return 0, NewError(CodeInvalidArgument, "negative tick count")
	}
	if sampleRate == 0 || tickRate == 0 {
		return 0, NewError(CodeInvalidArgument, "zero timebase denominator")
	}
	hi, lo := bits.Mul64(uint64(ticks), uint64(sampleRate))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, NewError(CodeOverflow, "tick-to-sample multiplication overflow")
	}
	return int64(lo) / int64(tickRate), nil
}

// Convert scales v from one rational unit to another with overflow checking.
// It rounds toward zero, which is floor for all supported non-negative uses.
func Convert(v int64, from, to Rational) (int64, error) {
	if from.Num <= 0 || from.Den <= 0 || to.Num <= 0 || to.Den <= 0 {
		return 0, NewError(CodeInvalidArgument, "rational must be positive")
	}
	if v < 0 {
		return 0, NewError(CodeInvalidArgument, "negative rational value")
	}
	// Total duration is v * from.Num / from.Den. Expressing it in units
	// whose duration is to.Num / to.Den gives:
	// v * from.Num * to.Den / (from.Den * to.Num)
	hi, lo := bits.Mul64(uint64(v), uint64(from.Num))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, NewError(CodeOverflow, "rational conversion overflow")
	}
	x := int64(lo)
	hi, lo = bits.Mul64(uint64(x), uint64(to.Den))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, NewError(CodeOverflow, "rational conversion overflow")
	}
	num := int64(lo)
	hi, lo = bits.Mul64(uint64(from.Den), uint64(to.Num))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, NewError(CodeOverflow, "rational denominator overflow")
	}
	den := int64(lo)
	return num / den, nil
}

// CheckedMul returns a*b when it fits in int64.
func CheckedMul(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a == math.MinInt64 || b == math.MinInt64 {
		return 0, false
	}
	r := a * b
	if r/b != a {
		return 0, false
	}
	return r, true
}
