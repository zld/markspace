// Package media provides the unified media time base shared by every
// subsystem. No subsystem may rebuild its own timeline; all timestamps
// are expressed as Ticks at an explicit TimeScale and converted through
// a Clock.
package media

import "fmt"

// Ticks is a signed count of units at a given TimeScale.
type Ticks int64

// TimeScale is units per second (e.g. 90000 for RTP video, 48000 for audio).
type TimeScale uint32

// RTP video clock per RFC 3550 convention for H.264/H.265/AV1.
const VideoScale TimeScale = 90000

// Clock converts timestamps between time scales and unwraps wrapping
// counters (RTP uint32 timestamps, uint16 sequence numbers). It is the
// single authority for media time; zero value is unusable, build via New.
type Clock struct {
	scale TimeScale
}

// New returns a Clock for the given scale. Scale must be non-zero.
func New(scale TimeScale) (Clock, error) {
	if scale == 0 {
		return Clock{}, fmt.Errorf("media: zero time scale")
	}
	return Clock{scale: scale}, nil
}

// Must is New for compile-time-known scales; panics on invalid scale.
func Must(scale TimeScale) Clock {
	c, err := New(scale)
	if err != nil {
		panic(err)
	}
	return c
}

// Scale returns the clock's time scale.
func (c Clock) Scale() TimeScale { return c.scale }

// ToMillis converts ticks at this clock's scale to milliseconds.
func (c Clock) ToMillis(t Ticks) int64 {
	return int64(t) * 1000 / int64(c.scale)
}

// FromMillis converts milliseconds to ticks at this clock's scale.
func (c Clock) FromMillis(ms int64) Ticks {
	return Ticks(ms * int64(c.scale) / 1000)
}

// Convert re-expresses t (in this clock's scale) in another scale,
// rounding toward zero. The instant is preserved; the epoch is shared.
func (c Clock) Convert(t Ticks, to TimeScale) Ticks {
	return Ticks(int64(t) * int64(to) / int64(c.scale))
}

// Unwrapper extends a wrapping uint32 counter (RTP timestamp) into a
// monotonically non-decreasing int64, tolerating reordering within half
// the wrap period. One Unwrapper per stream; not safe for concurrent use.
type Unwrapper struct {
	cycle   int64
	last    uint32
	started bool
}

// Unwrap extends raw into 64-bit time. The first call anchors the epoch.
// Reordered packets within half a wrap (2^31) resolve to the same cycle.
func (u *Unwrapper) Unwrap(raw uint32) int64 {
	if !u.started {
		u.started = true
		u.last = raw
		return u.cycle + int64(raw)
	}
	delta := raw - u.last // modular arithmetic handles wrap
	if raw >= u.last {
		// forward (or duplicate within same cycle)
		if delta > 1<<31 {
			return u.cycle - (1 << 32) + int64(raw) // late packet from previous cycle
		}
	} else {
		// raw < last: either wrapped forward or reordered backward
		if delta <= 1<<31 {
			u.cycle += 1 << 32 // wrapped forward
		}
		// else: reordered old packet, same cycle
	}
	u.last = raw
	return u.cycle + int64(raw)
}

// SeqUnwrapper extends wrapping uint16 RTP sequence numbers.
type SeqUnwrapper struct {
	cycle   int64
	last    uint16
	started bool
}

// Unwrap extends a uint16 sequence number into 64-bit sequence space.
func (s *SeqUnwrapper) Unwrap(raw uint16) int64 {
	if !s.started {
		s.started = true
		s.last = raw
		return int64(raw)
	}
	delta := raw - s.last
	if raw < s.last && delta <= 1<<15 {
		s.cycle += 1 << 16
	} else if raw >= s.last && delta > 1<<15 {
		return s.cycle - (1 << 16) + int64(raw)
	}
	s.last = raw
	return s.cycle + int64(raw)
}
