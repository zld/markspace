package media

import (
	"fmt"
)

// DefaultTimescale is the RTP video clock rate, in ticks per second.
const DefaultTimescale uint32 = 90000

const (
	wrapPeriod int64 = 1 << 32
	wrapHalf   int64 = 1 << 31
)

// UnwrapDelta returns cur-prev as a signed tick delta, interpreting a step
// larger than half of the 32-bit tick space as a wraparound.
func UnwrapDelta(prev, cur uint32) int64 {
	d := int64(cur) - int64(prev)
	if d < -wrapHalf {
		return d + wrapPeriod
	}
	if d > wrapHalf {
		return d - wrapPeriod
	}
	return d
}

// Point is the result of observing one raw 32-bit media timestamp.
type Point struct {
	// Ticks is the monotonic 64-bit media time since the first observation.
	Ticks int64
	// Wrapped reports that this observation crossed the 32-bit boundary.
	Wrapped bool
	// Regressed reports that the observation moved backwards by more than the
	// configured tolerance relative to the highest time seen so far.
	Regressed bool
}

// Clock converts raw 32-bit media timestamps to a monotonic 64-bit timeline,
// handles wraparound, and compares times with an explicit tolerance.
//
// A Clock is not safe for concurrent use; each stream owns one clock.
type Clock struct {
	timescale uint32
	lastRaw   uint32
	lastTicks int64
	highTicks int64
	tolerance int64
	seen      bool
}

// NewClock returns a clock for the given ticks-per-second timescale.
func NewClock(timescale uint32) (*Clock, error) {
	if timescale == 0 {
		return nil, New(CatInvalidParams, StageScan, "timescale must be positive")
	}
	return &Clock{timescale: timescale}, nil
}

// MustClock is NewClock for compile-time constant timescales in tests and
// fixtures; it panics on an invalid timescale.
func MustClock(timescale uint32) *Clock {
	c, err := NewClock(timescale)
	if err != nil {
		panic(err)
	}
	return c
}

// Timescale returns the clock's ticks per second.
func (c *Clock) Timescale() uint32 { return c.timescale }

// SetToleranceTicks sets how far a timestamp may move backwards before it is
// reported as a regression rather than ordinary jitter or reordering.
func (c *Clock) SetToleranceTicks(ticks int64) {
	if ticks < 0 {
		ticks = 0
	}
	c.tolerance = ticks
}

// SetToleranceMS sets the regression tolerance expressed in milliseconds.
func (c *Clock) SetToleranceMS(ms int64) {
	if ms <= 0 {
		c.tolerance = 0
		return
	}
	c.tolerance = c.FromMS(ms)
}

// Observe folds one raw timestamp into the monotonic timeline. The first
// observation defines tick zero. The returned Point reports both 32-bit wrap
// events and backward movement beyond the tolerance.
func (c *Clock) Observe(raw uint32) Point {
	if !c.seen {
		c.seen = true
		c.lastRaw = raw
		c.lastTicks = 0
		c.highTicks = 0
		return Point{}
	}
	delta := UnwrapDelta(c.lastRaw, raw)
	ticks := c.lastTicks + delta
	rawDelta := int64(raw) - int64(c.lastRaw)
	wrapped := (rawDelta < 0 && delta > 0) || (rawDelta > 0 && delta < 0)
	p := Point{Ticks: ticks, Wrapped: wrapped}
	if ticks < c.highTicks-c.tolerance {
		p.Regressed = true
	}
	if ticks > c.highTicks {
		c.highTicks = ticks
	}
	c.lastRaw = raw
	c.lastTicks = ticks
	return p
}

// ToMS converts a tick count to milliseconds, rounding half away from zero.
func (c *Clock) ToMS(ticks int64) int64 {
	return divRound(ticks*1000, int64(c.timescale))
}

// FromMS converts milliseconds to ticks, rounding half away from zero.
func (c *Clock) FromMS(ms int64) int64 {
	return divRound(ms*int64(c.timescale), 1000)
}

// DurationMS returns end-start in milliseconds, clamped at zero.
func (c *Clock) DurationMS(startTicks, endTicks int64) int64 {
	if endTicks <= startTicks {
		return 0
	}
	return divRound((endTicks-startTicks)*1000, int64(c.timescale))
}

// WithinTicks reports whether a and b are at most tol ticks apart.
func (c *Clock) WithinTicks(a, b, tol int64) bool {
	return absDiff(a, b) <= tol
}

// WithinMS reports whether a and b are at most tolMS milliseconds apart.
// It compares in tick space so a millisecond tolerance is not silently
// rounded away by the conversion.
func (c *Clock) WithinMS(a, b, tolMS int64) bool {
	if tolMS < 0 {
		tolMS = 0
	}
	return absDiff(a, b)*1000 <= tolMS*int64(c.timescale)
}

// CompareTicks orders a and b, treating a difference within tol as equal.
// It returns -1, 0, or 1.
func CompareTicks(a, b, tol int64) int {
	if tol < 0 {
		tol = 0
	}
	switch d := a - b; {
	case d < -tol:
		return -1
	case d > tol:
		return 1
	default:
		return 0
	}
}

// MustTimescale validates a timescale supplied by configuration.
func MustTimescale(timescale uint32) (uint32, error) {
	if timescale == 0 {
		return 0, fmt.Errorf("timescale must be positive")
	}
	return timescale, nil
}

func divRound(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	if n >= 0 {
		return (n + d/2) / d
	}
	return -((-n + d/2) / d)
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
