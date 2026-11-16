// Package buffer implements the shared jitter buffer controller used by
// both the UDP and trace ingress paths. It sorts and deduplicates packets
// by 16-bit sequence number (wrap-safe), adapts the target water level to
// the smoothed arrival jitter, and serves frames to the playback clock
// without ever blocking it.
package buffer

import (
	"math"
	"sync"
	"time"

	"jitter/internal/clock"
	"jitter/internal/wire"
)

const (
	// WindowSize is the reorder window in frames; packets further ahead of
	// the newest sequence number are rejected.
	WindowSize = 64
	// MaxQueueFrames is the hard cap on buffered frames.
	MaxQueueFrames = 150
	// MinTarget / MaxTarget bound the adaptive target water level.
	MinTarget = 2
	MaxTarget = 12

	frameInterval = time.Duration(wire.FrameMs) * time.Millisecond
	stallRealign  = 300 * time.Millisecond

	growHysteresis   = 3  // consecutive jitter samples above target to grow
	shrinkHysteresis = 12 // consecutive samples below target to shrink
	missGrowStreak   = 4  // consecutive playback misses that force a grow
	calmShrinkStreak = 50 // consecutive hits before shrinking after misses
)

// Stats counts packet fate; all fields are monotonic counters.
type Stats struct {
	Received    uint64 // accepted into the buffer
	Duplicates  uint64 // already buffered
	Late        uint64 // already played/skipped
	OutOfWindow uint64 // beyond WindowSize ahead
	Reordered   uint64 // accepted out of order within the window
	DroppedFull uint64 // queue cap hit, dropped instead of blocking
	Realigned   uint64 // timestamp-jump or stall resyncs
}

type Controller struct {
	clk clock.Clock

	mu      sync.Mutex
	buf     map[uint16]wire.Packet
	started bool // at least one packet anchored
	playing bool // initial buffering done
	maxSeq  uint16
	playSeq uint16

	seqAnchor uint16
	tsAnchor  uint32

	target   int
	jitterMs float64

	lastInOrderArrival time.Duration
	lastInput          time.Duration

	grow, shrink     int
	missStreak, calm int

	stats Stats
}

func New(clk clock.Clock) *Controller {
	return &Controller{clk: clk, buf: make(map[uint16]wire.Packet), target: MinTarget}
}

// seqBehind reports whether a is behind b in wrap-safe 16-bit sequence space.
func seqBehind(a, b uint16) bool { return int16(a-b) < 0 }

// Push accepts one decoded packet. It never blocks the playback clock:
// contention only costs a short mutex wait and a full queue drops the
// packet instead of waiting.
func (c *Controller) Push(p wire.Packet) {
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		c.anchor(p, now)
		return
	}
	// Timestamp jump (restart, SSRC switch, 32-bit wrap handled by int32):
	// realign instead of mixing timelines.
	expected := c.tsAnchor + uint32(p.Seq-c.seqAnchor)*uint32(wire.SamplesPerFrame)
	if d := int32(p.Timestamp - expected); d > wire.SamplesPerFrame/2 || d < -wire.SamplesPerFrame/2 {
		c.realign(p, now)
		return
	}
	switch diff := int16(p.Seq - c.maxSeq); {
	case diff > 0:
		if diff > WindowSize {
			c.stats.OutOfWindow++
			return
		}
		if diff == 1 {
			dev := math.Abs(float64(now-c.lastInOrderArrival-frameInterval)) / float64(time.Millisecond)
			c.jitterMs += (dev - c.jitterMs) / 16
			c.lastInOrderArrival = now
		}
		c.maxSeq = p.Seq
	default:
		if seqBehind(p.Seq, c.playSeq) {
			c.stats.Late++
			return
		}
		if _, ok := c.buf[p.Seq]; ok {
			c.stats.Duplicates++
			return
		}
		c.stats.Reordered++
	}
	if len(c.buf) >= MaxQueueFrames {
		c.stats.DroppedFull++
		return
	}
	c.buf[p.Seq] = p
	c.stats.Received++
	c.lastInput = now
	c.adjustTarget()
}

// Poll returns the frame due at the current playback deadline. A missing
// frame is skipped (never replayed late in a burst); the caller is expected
// to conceal the gap.
func (c *Controller) Poll() (wire.Packet, bool) {
	now := c.clk.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return wire.Packet{}, false
	}
	if len(c.buf) == 0 && now-c.lastInput > stallRealign {
		// Input stalled: resync to the next arriving packet instead of
		// emitting a burst of expired frames.
		c.started, c.playing = false, false
		c.stats.Realigned++
		return wire.Packet{}, false
	}
	if !c.playing {
		if len(c.buf) < c.target {
			return wire.Packet{}, false
		}
		c.playing = true
	}
	if p, ok := c.buf[c.playSeq]; ok {
		delete(c.buf, c.playSeq)
		c.playSeq++
		c.missStreak = 0
		if c.target > MinTarget {
			if c.calm++; c.calm >= calmShrinkStreak {
				c.target--
				c.calm = 0
			}
		}
		return p, true
	}
	c.playSeq++ // skip the missing frame
	c.calm = 0
	if c.missStreak++; c.missStreak >= missGrowStreak && c.target < MaxTarget {
		c.target++
		c.missStreak = 0
	}
	return wire.Packet{}, false
}

// anchor starts (or restarts) the timeline at p.
func (c *Controller) anchor(p wire.Packet, now time.Duration) {
	c.started = true
	c.playing = false
	c.maxSeq, c.playSeq = p.Seq, p.Seq
	c.seqAnchor, c.tsAnchor = p.Seq, p.Timestamp
	c.lastInOrderArrival, c.lastInput = now, now
	c.buf[p.Seq] = p
	c.stats.Received++
}

// realign drops the buffered timeline and re-anchors at p after a
// timestamp jump.
func (c *Controller) realign(p wire.Packet, now time.Duration) {
	c.buf = make(map[uint16]wire.Packet)
	c.stats.Realigned++
	c.anchor(p, now)
}

// adjustTarget moves the target water level toward the jitter-derived want
// level, one frame at a time, with hysteresis in both directions.
func (c *Controller) adjustTarget() {
	want := MinTarget + int(c.jitterMs/8)
	if want < MinTarget {
		want = MinTarget
	}
	if want > MaxTarget {
		want = MaxTarget
	}
	switch {
	case want > c.target:
		c.grow, c.shrink = c.grow+1, 0
		if c.grow >= growHysteresis {
			c.target++
			c.grow = 0
		}
	case want < c.target:
		c.shrink, c.grow = c.shrink+1, 0
		if c.shrink >= shrinkHysteresis {
			c.target--
			c.shrink = 0
		}
	default:
		c.grow, c.shrink = 0, 0
	}
}

// Queued is the current number of buffered frames.
func (c *Controller) Queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.buf)
}

// Target is the current target water level in frames.
func (c *Controller) Target() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.target
}

// Playing reports whether initial buffering has completed.
func (c *Controller) Playing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.playing
}

// Stats returns a snapshot of the counters.
func (c *Controller) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
