// Package timeline defines the single nanosecond time base shared by every
// subsystem (EBML block events, RTP jitter buffer, track normalization and
// the deterministic merger) together with the unified error taxonomy.
package timeline

import (
	"errors"
	"fmt"
)

// Sentinel errors shared across packages. Callers classify failures with
// errors.Is rather than string matching.
var (
	// ErrTruncated means an element or packet ended before its declared size.
	ErrTruncated = errors.New("truncated input")
	// ErrOverflow means a length or time computation would overflow int64.
	ErrOverflow = errors.New("integer overflow")
	// ErrOutOfBounds means an offset or length crosses the configured limit.
	ErrOutOfBounds = errors.New("integer out of bounds")
	// ErrLimitExceeded means an element is larger than the configured cap.
	ErrLimitExceeded = errors.New("element exceeds configured limit")
	// ErrIncompatible means a track cannot be normalized safely.
	ErrIncompatible = errors.New("incompatible track layout")
	// ErrGap means a packet gap survived the jitter window.
	ErrGap = errors.New("unrecovered packet gap")
	// ErrCancelled is returned after the pipeline receives a cancellation.
	ErrCancelled = errors.New("pipeline cancelled")
)

// RecoverLevel states how much of the asset remains usable after a fault.
type RecoverLevel int

const (
	// RecoverNone: structural input cannot be parsed at all.
	RecoverNone RecoverLevel = iota
	// RecoverPartial: parsing continues past the fault with recorded gaps.
	RecoverPartial
	// RecoverFull: the fault was repaired without loss of delivered events.
	RecoverFull
)

func (l RecoverLevel) String() string {
	switch l {
	case RecoverNone:
		return "none"
	case RecoverPartial:
		return "partial"
	case RecoverFull:
		return "full"
	default:
		return "unknown"
	}
}

// Error is the unified structured error type. It carries the byte or packet
// offset where the fault was observed and the recoverability level.
type Error struct {
	Subsystem string
	Op        string
	Offset    int64
	Level     RecoverLevel
	Detail    string
	Err       error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s", e.Subsystem, e.Op)
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap enables errors.Is against the sentinel taxonomy.
func (e *Error) Unwrap() error { return e.Err }

// NewError builds a structured error with a non-negative offset.
func NewError(subsystem, op string, offset int64, level RecoverLevel, detail string, cause error) *Error {
	return &Error{
		Subsystem: subsystem, Op: op, Offset: offset,
		Level: level, Detail: detail, Err: cause,
	}
}

// TimeBase converts source clock ticks into nanoseconds. All internal
// timestamps are int64 nanoseconds since the stream origin.
type TimeBase struct {
	// ClockRate is ticks per second, e.g. 90000 for RTP video, 1000 for
	// Matroska cluster timecodes. Must be > 0.
	ClockRate uint64
}

// ToNanos converts ticks to nanoseconds and rejects int64 overflow.
func (t TimeBase) ToNanos(ticks uint64) (int64, error) {
	if t.ClockRate == 0 {
		return 0, NewError("timeline", "to_nanos", -1, RecoverNone,
			"zero clock rate", ErrOverflow)
	}
	const nanosPerSec = uint64(1_000_000_000)
	hi, lo := mul64(ticks, nanosPerSec)
	if hi != 0 || lo > uint64(maxInt64) {
		return 0, NewError("timeline", "to_nanos", -1, RecoverNone,
			fmt.Sprintf("ticks=%d overflows nanosecond range", ticks), ErrOverflow)
	}
	return int64(lo / t.ClockRate), nil
}

// NanosToTicks converts nanoseconds back to source ticks, rounding down.
func (t TimeBase) NanosToTicks(nanos int64) (uint64, error) {
	if nanos < 0 || t.ClockRate == 0 {
		return 0, NewError("timeline", "nanos_to_ticks", -1, RecoverNone,
			"negative nanos or zero clock rate", ErrOverflow)
	}
	hi, lo := mul64(uint64(nanos), t.ClockRate)
	if hi != 0 || lo > uint64(maxInt64) {
		return 0, NewError("timeline", "nanos_to_ticks", -1, RecoverNone,
			"tick overflow", ErrOverflow)
	}
	return lo / 1_000_000_000, nil
}

// WrapClock unwraps a fixed-width cyclic counter into a monotonic uint64
// stream. It handles both 16-bit RTP sequence numbers and 32-bit RTP
// timestamps with a configurable modulus.
type WrapClock struct {
	modulus     uint64
	half        uint64
	initialized bool
	last        uint64 // last unwrapped value
}

// NewWrapClock returns a clock for the given bit width (16 or 32).
func NewWrapClock(bits uint) *WrapClock {
	m := uint64(1) << bits
	return &WrapClock{modulus: m, half: m / 2}
}

// Unwrap maps the next raw counter value to an unwrapped uint64 stream. A
// jump within half the modulus is interpreted as ordinary forward drift or,
// when negative, as reordered arrival rather than a wraparound; only a jump
// larger than half the modulus crosses the modulus boundary. The first
// observed value establishes the origin.
func (w *WrapClock) Unwrap(raw uint64) uint64 {
	raw %= w.modulus
	if !w.initialized {
		w.initialized = true
		w.last = raw
		return raw
	}
	base := (w.last / w.modulus) * w.modulus
	cand := base + raw
	diff := int64(raw - (w.last % w.modulus))
	switch {
	case diff < -int64(w.half):
		cand += w.modulus // wrapped forward across the modulus
	case diff > int64(w.half):
		cand -= w.modulus // large backward jump, treat as reorder
	}
	w.last = cand
	return cand
}

// Last returns the most recent unwrapped value.
func (w *WrapClock) Last() uint64 { return w.last }

// Advance applies a signed tick delta to an unwrapped counter, rejecting
// negative results and overflow. Used for Matroska relative block timecodes.
func Advance(base int64, delta int16) (int64, error) {
	v := base + int64(delta)
	if (delta > 0 && v < base) || (delta < 0 && v > base) {
		return 0, NewError("timeline", "advance", -1, RecoverNone,
			"block timecode overflow", ErrOverflow)
	}
	return v, nil
}

// CheckMonotonic enforces the post-merge invariant: timestamps on the same
// track must never move backwards.
func CheckMonotonic(prev, next int64, track uint32) error {
	if next < prev {
		return NewError("timeline", "monotonic", -1, RecoverPartial,
			fmt.Sprintf("track %d time regression %d -> %d", track, prev, next),
			ErrOutOfBounds)
	}
	return nil
}

const maxInt64 = int64(^uint64(0) >> 1)

// mul64 returns the 128-bit product of two uint64 values as hi, lo.
func mul64(a, b uint64) (hi, lo uint64) {
	const mask32 = uint64(1<<32 - 1)
	aLo, aHi := a&mask32, a>>32
	bLo, bHi := b&mask32, b>>32
	lo = aLo * bLo
	mid := aHi*bLo + aLo*bHi
	hi = aHi*bHi + (mid >> 32)
	lo += (mid & mask32) << 32
	if lo < (mid&mask32)<<32 {
		hi++
	}
	return hi, lo
}
