package clock

import (
	"math"

	"mediad/internal/mederr"
)

// RTPModulus is the 32-bit RTP timestamp modulus.
const RTPModulus int64 = 1 << 32

// Unwrapper expands 32-bit RTP timestamps into a monotonic 64-bit
// sequence. Forward wrap is detected when the raw value drops by more
// than half the modulus; any other backward movement is reported as a
// non-monotonic timestamp error and leaves the state untouched so the
// stream can continue.
type Unwrapper struct {
	started bool
	lastRaw uint32
	cycles  int64
}

// Unwrap extends raw into 64-bit monotonic ticks. It returns a
// *mederr.Error (category timeline) for duplicate or backward-moving
// timestamps; the offending packet should be skipped by the caller.
func (u *Unwrapper) Unwrap(raw uint32) (int64, error) {
	if !u.started {
		u.started = true
		u.lastRaw = raw
		return int64(raw), nil
	}
	last := u.lastRaw
	switch {
	case raw == last:
		return 0, mederr.New(mederr.CatTimeline, "rtp", -1, -1, true,
			"duplicate timestamp %d", raw)
	case raw > last:
		if uint64(raw-last) > math.MaxInt32 {
			return 0, mederr.New(mederr.CatTimeline, "rtp", -1, -1, true,
				"non-monotonic timestamp: %d after %d", raw, last)
		}
	default: // raw < last
		if uint64(last-raw) > math.MaxInt32 {
			u.cycles++ // forward wrap across the 32-bit boundary
		} else {
			return 0, mederr.New(mederr.CatTimeline, "rtp", -1, -1, true,
				"non-monotonic timestamp: %d after %d", raw, last)
		}
	}
	u.lastRaw = raw
	return u.cycles*RTPModulus + int64(raw), nil
}
