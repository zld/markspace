// Package media provides the shared time base, timestamp unwrapping and
// drift estimation primitives used by every other subsystem.
package media

import "errors"

// ErrInvalidTimeBase is returned when a time base declares a zero or
// negative tick rate.
var ErrInvalidTimeBase = errors.New("invalid time base: tick rate must be positive")

// TimeBase converts discrete media ticks (per-track clock units) into the
// unified nanosecond timeline and back. All conversions use integer math so
// that repeated round trips on the same value stay deterministic.
type TimeBase struct {
	// ticksPerSecond is the per-track clock rate (mdhd.timescale or RTP
	// clock frequency).
	ticksPerSecond uint32
}

// NewTimeBase constructs a TimeBase. A zero rate is rejected instead of
// being silently treated as a 1 ns clock.
func NewTimeBase(ticksPerSecond uint32) (TimeBase, error) {
	if ticksPerSecond == 0 {
		return TimeBase{}, ErrInvalidTimeBase
	}
	return TimeBase{ticksPerSecond: ticksPerSecond}, nil
}

// TicksPerSecond reports the clock rate.
func (t TimeBase) TicksPerSecond() uint32 { return t.ticksPerSecond }

// ToNanos converts a (possibly unwrapped) tick offset to nanoseconds.
func (t TimeBase) ToNanos(ticks int64) int64 {
	return ticks * 1_000_000_000 / int64(t.ticksPerSecond)
}

// FromNanos converts a nanosecond offset back to ticks, rounding to nearest.
func (t TimeBase) FromNanos(nanos int64) int64 {
	q := nanos * int64(t.ticksPerSecond)
	if q >= 0 {
		return (q + 500_000_000) / 1_000_000_000
	}
	return -((-q + 500_000_000) / 1_000_000_000)
}

// WrapLimit returns 2^bits, the modulus at which a counter of the given
// width wraps around.
func WrapLimit(bits uint) uint64 {
	if bits >= 64 {
		return 0 // 2^64 cannot be represented; callers treat 0 as "no wrap".
	}
	return uint64(1) << bits
}

// Wrapper expands modulo-2^bits timestamps (RTP: 32-bit; MP4 v0 boxes:
// 32-bit) into a monotonically non-decreasing tick stream. It performs no
// rate-based guessing: a jump smaller than half the modulus in either
// direction is treated as a forward/backward step, and large jumps are
// resolved in the wrap direction consistent with the smaller arc.
type Wrapper struct {
	limit  int64
	half   int64
	first  bool
	last   int64 // last unwrapped value
	lastRaw uint32
}

// NewWrapper builds a Wrapper for the given counter width (typically 32).
func NewWrapper(bits uint) *Wrapper {
	limit := WrapLimit(bits)
	return &Wrapper{limit: int64(limit), half: int64(limit / 2), first: true}
}

// Expand feeds one raw modulo value and returns its unwrapped absolute
// tick value. Equal inputs yield equal outputs; backwards steps are
// preserved (they do not force a monotonic result).
func (w *Wrapper) Expand(raw uint32) int64 {
	if w.first {
		w.first = false
		w.last = int64(raw)
		w.lastRaw = raw
		return w.last
	}
	delta := int64(raw) - int64(w.lastRaw)
	if delta > w.half {
		delta -= w.limit // wrapped forward
	} else if delta < -w.half {
		delta += w.limit // wrapped backward
	}
	w.last += delta
	w.lastRaw = raw
	return w.last
}

// Last returns the most recent unwrapped value, or 0 before the first input.
func (w *Wrapper) Last() int64 { return w.last }
