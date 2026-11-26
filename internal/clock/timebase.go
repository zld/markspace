// Package clock implements the 27 MHz unified time base shared by the parser,
// indexer and verifier. Raw PCR values are 42-bit counters that wrap at
// 2^33 base units; expansion turns them into a monotonically non-decreasing
// tick stream without ever fabricating timestamps.
package clock

import "errors"

const (
	// TicksPerSecond is the MPEG-TS 27 MHz program clock rate.
	TicksPerSecond = 27_000_000
	// PCRBaseModulus is the modulus of the 33-bit PCR base (90 kHz units).
	PCRBaseModulus = 1 << 33
	// PCRModulus is the full 42-bit PCR modulus expressed in 27 MHz ticks.
	PCRModulus = int64(PCRBaseModulus) * 300
	// PCRMax is the largest legal PCR value.
	PCRMax = PCRModulus - 1
)

// ErrBackwards indicates a small backward PCR jump that is not a wrap. The
// unified clock holds its previous value rather than inventing a timestamp.
var ErrBackwards = errors.New("clock: non-wrapping backwards PCR")

// Clock expands wrapping PCR values into unified 27 MHz ticks. A Clock is not
// safe for concurrent use.
type Clock struct {
	started bool
	lastPCR int64
	unified int64
	wraps   int64
}

// New returns an empty unified clock.
func New() *Clock { return &Clock{} }

// Expand maps one raw PCR to unified ticks. The boolean result reports whether
// a modulus wrap was observed. A small backward jump (not a wrap) returns
// ErrBackwards together with the last valid unified ticks.
func (c *Clock) Expand(pcr int64) (ticks int64, wrapped bool, err error) {
	if pcr < 0 || pcr > PCRMax {
		return 0, false, errors.New("clock: PCR out of range")
	}
	if !c.started {
		c.started = true
		c.lastPCR = pcr
		c.unified = pcr
		return pcr, false, nil
	}
	delta := pcr - c.lastPCR
	if delta < -PCRModulus/2 {
		// Backward jump larger than half the modulus: a real 2^33 wrap.
		delta += PCRModulus
		wrapped = true
		c.wraps++
	} else if delta < 0 {
		// Small discontinuity: keep the previous time, do not fabricate one.
		return c.unified, false, ErrBackwards
	}
	c.unified += delta
	c.lastPCR = pcr
	return c.unified, wrapped, nil
}

// Wraps returns the number of PCR wraps observed so far.
func (c *Clock) Wraps() int64 { return c.wraps }

// Ticks returns the most recent unified tick value.
func (c *Clock) Ticks() int64 { return c.unified }

// Seconds converts unified ticks to fractional seconds.
func Seconds(ticks int64) float64 { return float64(ticks) / float64(TicksPerSecond) }

// Nanoseconds converts unified ticks to whole nanoseconds.
func Nanoseconds(ticks int64) int64 { return ticks * 1_000_000_000 / TicksPerSecond }

// Anchor binds a source byte offset to the unified ticks known at that point.
type Anchor struct {
	Offset int64
	Ticks  int64
}

// Timeline remembers the latest PCR anchor so that packets without their own
// PCR can be stamped with the most recent known time. It never interpolates
// or invents timestamps: the stamp is always a previously observed value.
type Timeline struct {
	last Anchor
	ok   bool
}

// Update records a PCR anchor at the given source offset.
func (t *Timeline) Update(offset, ticks int64) Anchor {
	t.last = Anchor{Offset: offset, Ticks: ticks}
	t.ok = true
	return t.last
}

// At returns the newest anchor at or before the given source offset.
func (t *Timeline) At(offset int64) (Anchor, bool) {
	if !t.ok || t.last.Offset > offset {
		return Anchor{}, false
	}
	return t.last, true
}
