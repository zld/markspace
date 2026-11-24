package clock

import "fmt"

// WrapEvent marks a detected modulo-2^32 rollover. Offset is the ordinal
// position of the sample (within its source) at which it was observed.
type WrapEvent struct {
	Offset      int    `json:"offset"`
	FromWrapped uint32 `json:"from_wrapped"`
	ToWrapped   uint32 `json:"to_wrapped"`
	Expanded    int64  `json:"expanded_value"`
}

// Unwrapper expands 32-bit RTP-style timestamps into a monotonic 64-bit
// tick line. It distinguishes a real rollover (a small forward move that
// crossed 2^32) from a genuine backward jump, which must never be silently
// treated as a new segment boundary.
type Unwrapper struct {
	last      int64
	haveLast  bool
	wraps     []WrapEvent
	sample    int
	negJumps  int
	lastNegAt int
}

// NewUnwrapper returns a ready unwrapper.
func NewUnwrapper() *Unwrapper { return &Unwrapper{} }

const wrapMod int64 = 1 << 32

// Expand feeds one wrapped timestamp and returns its expanded value.
// A negative jump yields an error plus the raw expanded delta so callers
// can record the fault instead of resynchronizing implicitly.
func (u *Unwrapper) Expand(ts uint32) (int64, error) {
	if !u.haveLast {
		u.haveLast = true
		u.last = int64(ts)
		u.sample++
		return u.last, nil
	}
	prevWrapped := uint32(u.last)
	delta := int64(ts) - int64(prevWrapped)
	if delta < 0 {
		delta += wrapMod
	}
	expanded := u.last + delta

	// Rollover: the wrapped value moved backwards but the modular
	// forward delta stays inside the first half of the cycle.
	if ts < prevWrapped && delta > 0 && delta < wrapMod/2 {
		u.wraps = append(u.wraps, WrapEvent{
			Offset: u.sample, FromWrapped: prevWrapped,
			ToWrapped: ts, Expanded: expanded,
		})
	}

	// A modular delta beyond half a cycle is a genuine backward move
	// (RFC 3550 ordering rule), not a wrap. It is reported rather than
	// absorbed so it can never masquerade as a new segment.
	if delta > wrapMod/2 {
		u.negJumps++
		u.lastNegAt = u.sample
		u.sample++
		return expanded, fmt.Errorf("clock: negative timestamp jump at sample %d: %d -> %d",
			u.sample, prevWrapped, ts)
	}

	u.last = expanded
	u.sample++
	return expanded, nil
}

// Wraps returns all rollover events observed so far.
func (u *Unwrapper) Wraps() []WrapEvent {
	out := make([]WrapEvent, len(u.wraps))
	copy(out, u.wraps)
	return out
}

// NegativeJumps reports how many backward jumps were rejected.
func (u *Unwrapper) NegativeJumps() int { return u.negJumps }

// LastNegativeSample is the ordinal of the last rejected jump, or -1.
func (u *Unwrapper) LastNegativeSample() int {
	if u.negJumps == 0 {
		return -1
	}
	return u.lastNegAt
}
