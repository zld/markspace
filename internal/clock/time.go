// Package clock provides the shared 90 kHz media clock used by every stage.
//
// The clock intentionally exposes a very small API: a tick is one sample of a
// 90 kHz timebase, RTP timestamps are expanded from their 32 bit wire form by
// an Unwrapper, and every conversion that can overflow returns ErrOverflow so
// callers can mark the affected input as unalignable instead of guessing.
package clock

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	// TickRate is the fixed media timescale used for RTP reconstruction and
	// for the MP4 track written by the mux stage.
	TickRate = 90000

	// TicksPerMilli is the number of ticks in one millisecond.
	TicksPerMilli = TickRate / 1000

	// GapThresholdTicks is the default stall threshold (200 ms).
	GapThresholdTicks Tick = 18000

	fullWrap = int64(1) << 32
)

// ErrOverflow reports that a time conversion does not fit into Tick.
var ErrOverflow = errors.New("clock: overflow")

// Tick is an unwrapped media timestamp in 90 kHz units.
type Tick int64

// Millis returns the tick value rounded down to whole milliseconds.
func (t Tick) Millis() int64 { return int64(t) / TicksPerMilli }

// MillisFloat returns the tick value in milliseconds with sub-ms precision.
func (t Tick) MillisFloat() float64 {
	return float64(t) / float64(TicksPerMilli)
}

// Seconds returns the tick value in seconds.
func (t Tick) Seconds() float64 { return float64(t) / float64(TickRate) }

func (t Tick) String() string {
	return fmt.Sprintf("%d ticks (%.3f ms)", int64(t), t.MillisFloat())
}

// FromMillis converts wall-clock or duration milliseconds to ticks.
func FromMillis(ms int64) (Tick, error) {
	if ms > math.MaxInt64/TicksPerMilli || ms < math.MinInt64/TicksPerMilli {
		return 0, fmt.Errorf("%w: %d ms does not fit in ticks", ErrOverflow, ms)
	}
	return Tick(ms * TicksPerMilli), nil
}

// FromDuration converts a duration to ticks.
func FromDuration(d time.Duration) (Tick, error) {
	ns := int64(d)
	if ns > math.MaxInt64/TicksPerMilli && ns > 0 {
		return 0, fmt.Errorf("%w: %v does not fit in ticks", ErrOverflow, d)
	}
	if ns < math.MinInt64/TicksPerMilli && ns < 0 {
		return 0, fmt.Errorf("%w: %v does not fit in ticks", ErrOverflow, d)
	}
	// Sub-millisecond precision is kept: 1 ms of media time is 90 ticks.
	return Tick(ns * TicksPerMilli / int64(time.Millisecond)), nil
}// Add returns a+b, or ErrOverflow when the sum cannot be represented.
func Add(a, b Tick) (Tick, error) {
	sum := int64(a) + int64(b)
	if (b > 0 && sum < int64(a)) || (b < 0 && sum > int64(a)) {
		return 0, fmt.Errorf("%w: %d + %d", ErrOverflow, int64(a), int64(b))
	}
	return Tick(sum), nil
}

// Sub returns a-b, or ErrOverflow when the difference cannot be represented.
func Sub(a, b Tick) (Tick, error) {
	if b == math.MinInt64 {
		return 0, fmt.Errorf("%w: %d - %d", ErrOverflow, int64(a), int64(b))
	}
	return Add(a, Tick(-int64(b)))
}

// Validate rejects values that cannot be produced by a 64 bit unwrap chain.
func (t Tick) Validate() error {
	if int64(t) <= math.MinInt64/2 || int64(t) >= math.MaxInt64/2 {
		return fmt.Errorf("%w: %d out of safe range", ErrOverflow, int64(t))
	}
	return nil
}

// Unwrapper expands 32 bit RTP timestamps into a monotonic 64 bit tick value.
//
// The expansion uses the signed 32 bit difference between the previous and the
// current timestamp, which is the standard RTP reordering rule: a difference
// greater than 2^31 in the unsigned domain is interpreted as a wrap. Out of
// order timestamps simply produce a value below the previous one; the caller
// decides whether that is acceptable for its timeline.
type Unwrapper struct {
	started bool
	last    uint32
	ext     int64
}

// Reset clears the unwrap state.
func (u *Unwrapper) Reset() {
	u.started = false
	u.last = 0
	u.ext = 0
}

// Unwrap returns the expanded timestamp for ts.
func (u *Unwrapper) Unwrap(ts uint32) (Tick, error) {
	if !u.started {
		u.started = true
		u.last = ts
		u.ext = int64(ts)
	} else {
		delta := int64(int32(ts - u.last)) // signed 32 bit delta
		next := u.ext + delta
		if next > math.MaxInt64/2 || next < math.MinInt64/2 {
			return 0, fmt.Errorf("%w: extended timestamp %d", ErrOverflow, next)
		}
		u.ext = next
		u.last = ts
	}
	return Tick(u.ext), nil
}

// Extend is the stateless form of Unwrap. It expands ts relative to a previous
// 32 bit timestamp and reports whether the 32 bit space wrapped.
func Extend(prev, ts uint32) (ext int64, wrapped bool) {
	delta := int64(int32(ts - prev))
	ext = int64(prev) + delta
	return ext, ts < prev && delta > 0
}
