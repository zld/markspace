// Package timeline defines the single media time axis used by every subsystem.
//
// All media time values inside the pipeline are represented as Time values in
// microseconds since the start of the unified timeline. Container-local
// timestamps (which may use arbitrary timescales and a non-zero media time
// zero), RTP timestamps (clock-rate dependent) and edit-list offsets are all
// converted onto this axis at the boundary of their subsystem and never
// interpreted as absolute media time afterwards.
package timeline

import (
	"errors"
	"fmt"
)

// Time is the unified time unit: microseconds on the media timeline.
type Time int64

// Unit constants. A microsecond is the smallest addressable time unit.
const (
	Microsecond Time = 1
	Millisecond Time = 1_000 * Microsecond
	Second      Time = 1_000 * Millisecond
	Minute      Time = 60 * Second
	Hour        Time = 60 * Minute
)

// Limits protect every conversion against int64 overflow. The maximum media
// duration the pipeline accepts is 24 hours; anything beyond is rejected as an
// out-of-range timestamp rather than silently wrapping.
const (
	MaxTime      Time = 24 * Hour
	MinTime      Time = -24 * Hour
	MaxTimescale      = 1 << 30 // 1 GHz is already absurd for media.
)

// Kind classifies every pipeline error so that callers can map failures to
// documented process exit codes without string matching.
type Kind uint8

const (
	KindUnknown Kind = iota
	// KindInvalidInput: malformed manifest, bad arguments, unsafe paths (exit 2).
	KindInvalidInput
	// KindCorrupt: damaged media/index/container that may be recoverable (exit 3).
	KindCorrupt
	// KindInternal: invariant violation inside the pipeline itself (exit 1).
	KindInternal
	// KindLimit: a configured safety/size limit was exceeded (exit 2).
	KindLimit
)

// Error is the single error type carrying a machine-readable classification.
type Error struct {
	Kind Kind
	Msg  string
	Err  error // optional wrapped cause
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds a classified error.
func NewError(kind Kind, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// WrapError classifies an existing error.
func WrapError(kind Kind, msg string, cause error) error {
	return &Error{Kind: kind, Msg: msg, Err: cause}
}

// Classify returns the Kind of err, or KindUnknown if it is not a *Error.
func Classify(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindUnknown
}

// FromScale converts a count expressed in timescale units-per-second onto the
// unified microsecond axis, using half-away-from-zero rounding. A zero or
// oversized timescale is invalid; results outside [-24h, 24h] are rejected.
func FromScale(units int64, timescale uint32) (Time, error) {
	if timescale == 0 {
		return 0, NewError(KindInvalidInput, "zero timescale in time-base conversion")
	}
	if timescale > MaxTimescale {
		return 0, NewError(KindLimit, "timescale %d exceeds limit %d", timescale, MaxTimescale)
	}
	num := units * 1_000_000
	if units != 0 && num/1_000_000 != units {
		return 0, NewError(KindLimit, "timestamp overflow converting %d units", units)
	}
	den := int64(timescale)
	q := num / den
	r := num % den
	if r < 0 {
		r = -r
	}
	if 2*r >= den { // half away from zero
		if num >= 0 {
			q++
		} else {
			q--
		}
	}
	t := Time(q)
	if t < MinTime || t > MaxTime {
		return 0, NewError(KindLimit, "converted time %d us outside allowed range", t)
	}
	return t, nil
}

// ToScale converts a unified-axis time back into units of the given timescale,
// again rounding half away from zero.
func ToScale(t Time, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, NewError(KindInvalidInput, "zero timescale in time-base conversion")
	}
	if timescale > MaxTimescale {
		return 0, NewError(KindLimit, "timescale %d exceeds limit %d", timescale, MaxTimescale)
	}
	num := int64(t) * int64(timescale)
	if t != 0 && num/int64(timescale) != int64(t) {
		return 0, NewError(KindLimit, "timestamp overflow converting %d us", t)
	}
	q := num / 1_000_000
	r := num % 1_000_000
	if r < 0 {
		r = -r
	}
	if 2*r >= 1_000_000 {
		if num >= 0 {
			q++
		} else {
			q--
		}
	}
	return q, nil
}

// Round returns t rounded to the nearest multiple of step (half away from
// zero). step must be positive.
func Round(t Time, step Time) (Time, error) {
	if step <= 0 {
		return 0, NewError(KindInvalidInput, "rounding step must be positive, got %d", step)
	}
	q := t / step
	r := t % step
	if r < 0 {
		r = -r
	}
	if 2*r >= step {
		if t >= 0 {
			q++
		} else {
			q--
		}
	}
	return q * step, nil
}

// Add adds two axis times with overflow detection.
func Add(a, b Time) (Time, error) {
	r := a + b
	if (b > 0 && r < a) || (b < 0 && r > a) {
		return 0, NewError(KindLimit, "time overflow adding %d + %d", a, b)
	}
	if r < MinTime || r > MaxTime {
		return 0, NewError(KindLimit, "time %d outside allowed range", r)
	}
	return r, nil
}

// Sub subtracts two axis times with overflow detection.
func Sub(a, b Time) (Time, error) {
	r := a - b
	if (b > 0 && r > a) || (b < 0 && r < a) {
		return 0, NewError(KindLimit, "time overflow subtracting %d - %d", a, b)
	}
	if r < MinTime || r > MaxTime {
		return 0, NewError(KindLimit, "time %d outside allowed range", r)
	}
	return r, nil
}

// Millis is a convenience accessor for reports and tests.
func (t Time) Millis() int64 { return int64(t / Millisecond) }
