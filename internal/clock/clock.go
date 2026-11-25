// Package clock is the shared monotonic media timeline. It expands the
// 33-bit, 90 kHz PCR/PTS/DTS counters of MPEG-TS into unwrapped uint64 ticks
// so that a PCR wrap never makes segment boundaries travel backwards.
package clock

import "sync"

// TickRate is the MPEG-TS presentation/PCR-base clock rate in Hz.
const TickRate uint64 = 90000

// Modulus33 is the wrap modulus of the 33-bit PCR base / PTS / DTS fields.
const Modulus33 uint64 = 1 << 33

// Tick is an unwrapped 90 kHz media timestamp.
type Tick uint64

// UnwrapNext extends the unwrapped previous value with a freshly read wrapped
// sample. It chooses the nearest in-range continuation, which correctly
// expands a forward wrap (e.g. 2^33-1 -> 0) and tolerates small jitter.
func UnwrapNext(prev uint64, havePrev bool, raw uint64, modulus uint64) uint64 {
	raw %= modulus
	if !havePrev {
		return raw
	}
	prevRaw := prev % modulus
	d := int64(raw) - int64(prevRaw)
	half := int64(modulus / 2)
	if d > half {
		d -= int64(modulus)
	} else if d < -half {
		d += int64(modulus)
	}
	next := int64(prev) + d
	if next < 0 {
		return raw
	}
	return uint64(next)
}

// Tracker unwraps one wrapped counter family.
type Tracker struct {
	modulus  uint64
	have     bool
	last     uint64
	wraps    int
	lastWrap bool
}

// NewTracker creates a tracker with the given wrap modulus.
func NewTracker(modulus uint64) *Tracker {
	if modulus == 0 {
		modulus = Modulus33
	}
	return &Tracker{modulus: modulus}
}

// Feed consumes one raw counter value and returns the unwrapped value plus
// whether this sample crossed the wrap point.
func (t *Tracker) Feed(raw uint64) (uint64, bool) {
	raw %= t.modulus
	if !t.have {
		t.have = true
		t.last = raw
		t.lastWrap = false
		return t.last, false
	}
	prevRaw := t.last % t.modulus
	d := int64(raw) - int64(prevRaw)
	half := int64(t.modulus / 2)
	crossed := false
	if d < -half {
		d += int64(t.modulus)
		crossed = true
	} else if d > half {
		d -= int64(t.modulus)
	}
	next := int64(t.last) + d
	if next < 0 {
		next = int64(raw)
	}
	t.last = uint64(next)
	if crossed {
		t.wraps++
	}
	t.lastWrap = crossed
	return t.last, crossed
}

// Last returns the most recent unwrapped value.
func (t *Tracker) Last() (uint64, bool) { return t.last, t.have }

// Wraps is the number of forward wraps observed.
func (t *Tracker) Wraps() int { return t.wraps }

// MediaClock is the concurrency-safe shared timeline used by parsing and
// fan-out. PCR anchors "now"; PTS/DTS are unwrapped onto the same tick scale.
type MediaClock struct {
	mu     sync.Mutex
	pcr    Tracker
	pts    Tracker
	dts    Tracker
	origin uint64
	hasOr  bool
	now    uint64
}

// NewMediaClock returns a clock using the standard 33-bit wrap modulus.
func NewMediaClock() *MediaClock {
	m := Modulus33
	return &MediaClock{pcr: *NewTracker(m), pts: *NewTracker(m), dts: *NewTracker(m)}
}

// FeedPCR consumes a raw 33-bit PCR base and returns unwrapped ticks.
func (c *MediaClock) FeedPCR(base uint64) (Tick, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, wrapped := c.pcr.Feed(base)
	if !c.hasOr {
		c.origin, c.hasOr = v, true
	}
	c.now = v
	return Tick(v), wrapped
}

// FeedPTS unwraps a raw PTS value.
func (c *MediaClock) FeedPTS(raw uint64) Tick {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, _ := c.pts.Feed(raw)
	return Tick(v)
}

// FeedDTS unwraps a raw DTS value.
func (c *MediaClock) FeedDTS(raw uint64) Tick {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, _ := c.dts.Feed(raw)
	return Tick(v)
}

// Now is the latest unwrapped PCR value.
func (c *MediaClock) Now() Tick {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Tick(c.now)
}

// Origin is the first unwrapped PCR value.
func (c *MediaClock) Origin() Tick {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Tick(c.origin)
}

// Elapsed is Now - Origin in ticks.
func (c *MediaClock) Elapsed() Tick {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasOr || c.now < c.origin {
		return 0
	}
	return Tick(c.now - c.origin)
}

// CheckAgainstPCR compares an unwrapped PTS/DTS boundary against current PCR.
// Returns the signed delta and whether it lies within tolerance.
func (c *MediaClock) CheckAgainstPCR(ts Tick, tolerance Tick) (int64, bool) {
	now := c.Now()
	d := int64(ts) - int64(now)
	if d < 0 {
		d = -d
	}
	return int64(ts) - int64(now), Tick(d) <= tolerance
}

// PCRFull converts a 33-bit base + 9-bit extension to 27 MHz units.
func PCRFull(base uint64, ext uint16) uint64 { return base*300 + uint64(ext) }

// TicksToSeconds converts 90 kHz ticks to seconds.
func TicksToSeconds(t Tick) float64 { return float64(t) / float64(TickRate) }

// TickDelta returns a-b as a signed delta.
func TickDelta(a, b Tick) int64 { return int64(a) - int64(b) }
