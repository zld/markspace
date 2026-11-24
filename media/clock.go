package media

import "math"

// Rate90k is the unified media time base: 90,000 ticks per second, the
// reference clock used by MPEG-TS PTS/DTS and RTP video timestamps.
const Rate90k int64 = 90_000

// PCRRate is the 27 MHz Program Clock Reference rate.
const PCRRate int64 = 27_000_000

// Wrap33 is the modulus of a 33-bit counter (PCR base / extended PTS).
const Wrap33 int64 = 1 << 33

// Wrap32 is the modulus of a 32-bit counter (RTP timestamps).
const Wrap32 int64 = 1 << 32

// TicksToMillis converts 90 kHz ticks to fractional milliseconds.
func TicksToMillis(ticks float64) float64 { return ticks * 1000.0 / float64(Rate90k) }

// TicksToSeconds converts 90 kHz ticks to seconds.
func TicksToSeconds(ticks float64) float64 { return ticks / float64(Rate90k) }

// SecondsToTicks converts seconds to 90 kHz ticks (rounded).
func SecondsToTicks(sec float64) int64 { return int64(math.Round(sec * float64(Rate90k))) }

// PCRToTicks converts a 27 MHz PCR value (base*300+extension) into 90 kHz ticks.
func PCRToTicks(pcr27 int64) int64 { return pcr27 / 300 }

// Extend lifts a width-bit wrapped counter value onto a monotonic int64 axis.
// The raw counter's own scale is preserved: the first observed value is taken
// at face value and becomes the period anchor, so values near the counter
// boundary do not overflow into negatives. Subsequent values choose the
// candidate closest to prev, so genuine wrap-arounds are expanded while
// backwards jumps stay backwards (they remain smaller than prev and are
// detectable by the caller).
func Extend(prev, value uint64, width uint) int64 {
	mod := uint64(1) << width
	half := mod >> 1
	if prev == 0 && value != 0 {
		return int64(value)
	}
	base := (prev / mod) * mod
	cand := base + value
	if d := int64(cand - prev); d > int64(half) {
		cand -= mod
	} else if d < -int64(half) {
		cand += mod
	}
	return int64(cand)
}

// Extend33 extends a 33-bit value (PCR base / PTS).
func Extend33(prev int64, value uint64) int64 { return Extend(uint64(prev), value, 33) }

// Extend32 extends a 32-bit value (RTP timestamps / generic counters).
func Extend32(prev int64, value uint32) int64 { return Extend(uint64(prev), uint64(value), 32) }

// Extend16 extends a 16-bit value (RTP sequence numbers / TS CC counters).
func Extend16(prev int64, value uint16) int64 { return Extend(uint64(prev), uint64(value), 16) }

// Unwrapper tracks one wrapping counter and emits monotonic extended values.
type Unwrapper struct {
	Width       uint
	MaxBackstep int64 // tolerated backwards delta in ticks; 0 = none tolerated
	MaxJump     int64 // tolerated forward jump in ticks; 0 = no upper bound
	prev        int64
	started     bool
}

// NewUnwrapper builds an unwrapper for the given counter width (16/32/33 …).
func NewUnwrapper(width uint, maxBackstep, maxJump int64) *Unwrapper {
	return &Unwrapper{Width: width, MaxBackstep: maxBackstep, MaxJump: maxJump}
}

// Reset clears all history.
func (u *Unwrapper) Reset() { u.started = false; u.prev = 0 }

// Prev returns the last extended value, or 0 before the first sample.
func (u *Unwrapper) Prev() int64 { return u.prev }

// Push extends raw and checks the delta against the configured limits.
// On the first sample every value is accepted. A backwards step beyond
// MaxBackstep, or a forward jump beyond MaxJump, yields a ClassClock error.
func (u *Unwrapper) Push(raw uint64) (int64, error) {
	if !u.started {
		u.prev = Extend(0, raw, u.Width)
		u.started = true
		return u.prev, nil
	}
	ext := Extend(uint64(u.prev), raw, u.Width)
	delta := ext - u.prev
	switch {
	case delta < 0 && u.MaxBackstep > 0 && -delta > u.MaxBackstep:
		return ext, New(ClassClock, "unwrapper",
			"counter moved backwards beyond tolerance").WithRange(delta, 0)
	case delta > 0 && u.MaxJump > 0 && delta > u.MaxJump:
		return ext, New(ClassClock, "unwrapper",
			"counter jumped beyond drift tolerance").WithRange(0, delta)
	}
	u.prev = ext
	return ext, nil
}

// Clock is the shared 90 kHz time base for one pipeline run. Each elementary
// stream gets its own Unwrapper, all extended onto the same tick axis.
type Clock struct {
	rate        int64
	MaxBackstep int64
	MaxDrift    int64 // maximum |extended delta - nominal delta| in ticks
}

// NewClock builds a 90 kHz clock with backstep/drift guardrails in ticks.
func NewClock(maxBackstep, maxDriftTicks int64) *Clock {
	return &Clock{rate: Rate90k, MaxBackstep: maxBackstep, MaxDrift: maxDriftTicks}
}

// Rate returns the clock rate.
func (c *Clock) Rate() int64 { return c.rate }

// Millis converts extended ticks to milliseconds.
func (c *Clock) Millis(ticks int64) float64 {
	return float64(ticks) * 1000.0 / float64(c.rate)
}

// DriftError compares an observed delta against a nominal delta and reports a
// ClassClock error when the absolute deviation exceeds the configured bound.
func (c *Clock) DriftError(observed, nominal int64) error {
	d := observed - nominal
	if d < 0 {
		d = -d
	}
	if c.MaxDrift > 0 && d > c.MaxDrift {
		return New(ClassClock, "clock", "drift exceeds configured threshold").
			WithRange(nominal, observed)
	}
	return nil
}
