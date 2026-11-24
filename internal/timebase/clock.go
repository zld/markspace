// Package timebase unwraps 33-bit MPEG-TS timestamps (PCR/PTS) into
// monotonically increasing 64-bit counters so every node derives the same
// timeline from the same bytes. Reruns are deterministic: the unwrap rule is
// pure arithmetic on the raw value and the previous state.
package timebase

import "math"

// Mask33 is the modulus of the MPEG-TS 33-bit timestamp fields.
const Mask33 uint64 = 1 << 33

// HalfMod33 is the largest forward jump that is still treated as normal
// progress rather than a wrap.
const HalfMod33 uint64 = Mask33 / 2

// Clock33 maintains the unwrapped timeline of one 33-bit timestamp source
// (one PCR PID or one PTS track).
type Clock33 struct {
	lastRaw    uint64
	lastAbs    uint64
	wraps      uint64
	initialized bool
}

// Reset restores the clock to its empty state.
func (c *Clock33) Reset() { *c = Clock33{} }

// Initialized reports whether at least one sample has been observed.
func (c *Clock33) Initialized() bool { return c.initialized }

// Wraps returns the number of 33-bit wrap events observed so far.
func (c *Clock33) Wraps() uint64 { return c.wraps }

// LastRaw returns the most recent raw 33-bit value.
func (c *Clock33) LastRaw() (uint64, bool) {
	return c.lastRaw, c.initialized
}

// Absolute returns the most recent unwrapped value.
func (c *Clock33) Absolute() (uint64, bool) {
	return c.lastAbs, c.initialized
}

// Unwrap feeds a raw 33-bit value (already masked to 33 bits) and returns the
// absolute unwrapped value. A backwards jump larger than HalfMod33 is a
// wrap; smaller backwards jumps are reported as a regression by the caller
// through Regression.
func (c *Clock33) Unwrap(raw uint64) uint64 {
	raw &= Mask33 - 1
	if !c.initialized {
		c.initialized = true
		c.lastRaw, c.lastAbs = raw, raw
		return raw
	}
	abs := c.lastAbs
	switch {
	case raw == c.lastRaw:
		// repeated sample: timeline stands still
	case raw > c.lastRaw:
		abs += raw - c.lastRaw
	default:
		// raw < lastRaw: a wrap occurred.
		abs += (Mask33 - c.lastRaw) + raw
		c.wraps++
	}
	c.lastRaw, c.lastAbs = raw, abs
	return abs
}

// Regression reports whether the pair (prevRaw, raw) moves backwards without
// qualifying as a wrap. When raw decreases, the forward distance around the
// modulus is computed: a small forward distance means the sample crossed the
// 33-bit boundary (a legitimate wrap), while a distance beyond HalfMod33
// means the timeline really moved backwards, since interpreting it as a wrap
// would require the clock to jump nearly a full cycle. Repeated values are
// not regressions.
func Regression(prevRaw, raw uint64) bool {
	prevRaw &= Mask33 - 1
	raw &= Mask33 - 1
	if raw >= prevRaw {
		return false
	}
	forward := (Mask33 - prevRaw) + raw
	return forward > HalfMod33
}

// PCRTo27MHz expands a 42-bit PCR (33-bit 90 kHz base + 9-bit extension) into
// an absolute 27 MHz tick count, adding wrap cycles recorded for the base.
func PCRTo27MHz(base33, ext9, wraps uint64) uint64 {
	return (wraps*Mask33+base33)*300 + (ext9 & 0x1FF)
}

// Seconds90k converts a 90 kHz tick count to seconds as a float.
func Seconds90k(ticks uint64) float64 {
	return float64(ticks) / 90000.0
}

// Seconds27MHz converts a 27 MHz tick count to seconds as a float.
func Seconds27MHz(ticks uint64) float64 {
	return float64(ticks) / 27000000.0
}

// MaxAbsolute is the practical upper bound used by overflow-safe callers.
const MaxAbsolute = math.MaxUint64
