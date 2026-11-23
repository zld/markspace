// Package rtp unwraps 32-bit RTP timestamps onto a monotonic 64-bit
// clock and estimates capture-device drift from paired (wall clock,
// media clock) observations. All arithmetic is overflow-checked.
package rtp

import (
	"math"

	"mediatool/internal/timeline"
)

// wrapMod is the 2^32 modulus of the RTP timestamp field.
const wrapMod uint64 = 1 << 32

// halfWrap is the largest jump that is still explained by wraparound;
// anything larger in the backwards direction looks like a device reset.
const halfWrap uint64 = 1 << 31

// Unwrapper expands 32-bit timestamps into a monotonically non-decreasing
// 64-bit series. It is safe for concurrent-free, per-stream use only.
type Unwrapper struct {
	last        uint64
	hasLast     bool
	wrapCount   uint64
	Wraparounds int // number of legal 2^32 wraps observed
}

// Unwrap returns the expanded timestamp. A backwards value whose forward
// modular distance is <= 2^31 is treated as a legal wraparound; a larger
// backwards jump is reported as timestamp_backward so callers can treat
// it as a device restart rather than silently corrupting the clock.
func (u *Unwrapper) Unwrap(ts uint32) (uint64, error) {
	cur := uint64(ts)
	if !u.hasLast {
		u.hasLast = true
		u.last = cur
		return cur, nil
	}
	prev := u.last
	prevMod := prev % wrapMod
	forward := (cur - prevMod + wrapMod) % wrapMod
	if forward > halfWrap {
		// The value jumped backwards by more than half the modulus: a
		// restart cannot be explained by wraparound. Rebase and flag it.
		u.last = cur
		return cur, timeline.NewError(timeline.ClassTimestampBackward,
			"RTP timestamp moved backwards by more than 2^31; clock rebased")
	}
	if math.MaxUint64-forward < prev {
		return 0, timeline.NewError(timeline.ClassOverflow,
			"unwrapped RTP timestamp overflows uint64")
	}
	next := prev + forward
	if forward > 0 && cur < prevMod {
		u.Wraparounds++
		u.wrapCount++
	}
	u.last = next
	return next, nil
}

// WrapCount returns the number of modulus crossings consumed so far.
func (u *Unwrapper) WrapCount() uint64 { return u.wrapCount }

// Reset clears the unwrapper state after an explicit device restart.
func (u *Unwrapper) Reset() { *u = Unwrapper{} }
