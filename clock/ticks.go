// Package clock converts 32-bit RTP timestamps into a monotonic integer
// tick timeline and tracks event-time watermarks over it.
package clock

import (
	"errors"
	"fmt"
	"math"
)

// ErrNegativeAdvance marks an impossible backward timestamp move.
var ErrNegativeAdvance = errors.New("clock: impossible negative timestamp advance")

// Extender converts 32-bit RTP timestamps into monotonic int64 ticks.
// Forward wraparound is transparent; equal timestamps collapse onto the
// same tick; small backward jumps (reorder jitter) are clamped to keep
// the output monotonic, and jumps beyond the tolerance are rejected.
type Extender struct {
	// BackwardTolerance is the maximum backward jump, in ticks, absorbed
	// by clamping. Larger jumps return ErrNegativeAdvance.
	BackwardTolerance uint32

	init    bool
	last    uint32
	lastExt int64

	// Wraps counts forward wraparounds; Clamped counts absorbed
	// backward jitters.
	Wraps   int
	Clamped int
}

// NewExtender creates an Extender with the given backward tolerance.
func NewExtender(backwardTolerance uint32) *Extender {
	return &Extender{BackwardTolerance: backwardTolerance}
}

// Extend maps ts onto the monotonic tick line.
func (e *Extender) Extend(ts uint32) (int64, error) {
	if !e.init {
		e.init = true
		e.last = ts
		e.lastExt = int64(ts)
		return e.lastExt, nil
	}
	d := int32(ts - e.last) // wrap-aware signed delta
	if d >= 0 {
		if ts < e.last {
			e.Wraps++
		}
		e.lastExt += int64(d)
	} else {
		back := uint32(-d)
		if back > e.BackwardTolerance {
			return 0, fmt.Errorf("%w: -%d ticks", ErrNegativeAdvance, back)
		}
		e.Clamped++ // hold lastExt: output stays monotonic
	}
	e.last = ts
	return e.lastExt, nil
}

// ExtendLate maps a timestamp belonging to an already-declared-lost
// packet that arrived late onto its historical tick, without clamping
// and without disturbing the extender state. It rejects jumps beyond
// the backward tolerance.
func (e *Extender) ExtendLate(ts uint32) (int64, error) {
	if !e.init {
		return 0, errors.New("clock: no reference timestamp")
	}
	d := int32(ts - e.last)
	if d < 0 && uint32(-d) > e.BackwardTolerance {
		return 0, fmt.Errorf("%w: -%d ticks", ErrNegativeAdvance, uint32(-d))
	}
	return e.lastExt + int64(d), nil
}

// TicksToMillis converts ticks to milliseconds at the given media clock
// rate, detecting arithmetic overflow.
func TicksToMillis(ticks int64, clockRate uint32) (int64, error) {
	if clockRate == 0 {
		return 0, errors.New("clock: zero clock rate")
	}
	if ticks < 0 {
		return 0, errors.New("clock: negative ticks")
	}
	if ticks > math.MaxInt64/1000 {
		return 0, fmt.Errorf("clock: %d ticks overflows milliseconds", ticks)
	}
	return ticks * 1000 / int64(clockRate), nil
}

// MillisToTicks converts milliseconds to ticks, detecting overflow.
func MillisToTicks(ms int64, clockRate uint32) (int64, error) {
	if clockRate == 0 {
		return 0, errors.New("clock: zero clock rate")
	}
	if ms < 0 {
		return 0, errors.New("clock: negative milliseconds")
	}
	if ms > math.MaxInt64/int64(clockRate) {
		return 0, fmt.Errorf("clock: %d ms overflows ticks", ms)
	}
	return ms * int64(clockRate) / 1000, nil
}
