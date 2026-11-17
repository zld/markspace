// Package jitter implements the shared receive-side buffering controller
// used by both the UDP and trace paths: it estimates jitter, adapts the
// target buffer between 2 and 12 frames with hysteresis, classifies
// incoming packets, and decides playout vs concealment on each tick.
package jitter

import (
	"math"

	"jbcheck/internal/metrics"
	"jbcheck/internal/packet"
	"jbcheck/internal/plc"
)

// Config tunes the controller. DefaultConfig matches the demo profile.
type Config struct {
	MinTarget        int   // minimum buffer target, frames
	MaxTarget        int   // maximum buffer target, frames
	QueueCap         int   // hard queue bound, frames
	PrefillTimeoutMs int64 // max wait for prefill before starting anyway
	SilenceMs        int64 // no-packet interval that triggers re-buffering
	AdjustIntervalMs int64 // min interval between target adjustments
	ClockJumpMs      int64 // discontinuity that re-anchors the playout clock
}

func DefaultConfig() Config {
	return Config{
		MinTarget:        2,
		MaxTarget:        12,
		QueueCap:         16,
		PrefillTimeoutMs: 300,
		SilenceMs:        120,
		AdjustIntervalMs: 200,
		ClockJumpMs:      2000,
	}
}

// Controller is the shared jitter-buffer state machine.
type Controller struct {
	cfg Config
	q   *Queue
	st  *metrics.Stats
	plc *plc.Concealer

	target    int
	jitterEst float64 // RFC 3550 interarrival jitter estimate, ms
	lastTrans float64
	haveTrans bool

	haveSeq bool
	nextSeq uint16

	started         bool
	anchorMs        int64 // arrival time anchoring the current playout schedule
	prefillDeadline int64
	nextPlayMs      int64
	lastPacketMs    int64
	lastNowMs       int64
	lastAdjustMs    int64

	scratch [plc.FrameSamples]int16
}

func NewController(cfg Config, st *metrics.Stats, c *plc.Concealer) *Controller {
	return &Controller{
		cfg:    cfg,
		q:      NewQueue(cfg.QueueCap),
		st:     st,
		plc:    c,
		target: cfg.MinTarget,
	}
}

// Target returns the current buffer target in frames (2..12).
func (c *Controller) Target() int { return c.target }

// QueueLen returns the number of buffered frames.
func (c *Controller) QueueLen() int { return c.q.Len() }

// Started reports whether prefill completed and playout is running.
func (c *Controller) Started() bool { return c.started }

// IdleMs returns how long ago the last valid packet arrived.
func (c *Controller) IdleMs(nowMs int64) int64 { return nowMs - c.lastPacketMs }

// Add classifies and queues one decoded packet at arrival time nowMs.
func (c *Controller) Add(p packet.Packet, pcm []int16, nowMs int64) {
	c.checkClock(nowMs)
	c.st.Input++
	c.lastPacketMs = nowMs

	// RFC 3550 jitter estimate on transit time (timestamp is in samples).
	transit := float64(nowMs) - float64(p.Timestamp)/48.0
	if c.haveTrans {
		d := transit - c.lastTrans
		if d < 0 {
			d = -d
		}
		c.jitterEst += (d - c.jitterEst) / 16
	}
	c.lastTrans = transit
	c.haveTrans = true

	if !c.haveSeq {
		c.haveSeq = true
		c.nextSeq = p.Seq
		c.prefillDeadline = nowMs + c.cfg.PrefillTimeoutMs
	}
	if SeqLess(p.Seq, c.nextSeq) {
		c.st.LateDrop++
		return
	}
	// A packet arriving into an empty, not-yet-started buffer anchors the
	// next playout schedule (initial prefill and re-buffering alike).
	if !c.started && c.q.Len() == 0 {
		c.anchorMs = nowMs
	}
	switch err := c.q.Insert(Frame{Seq: p.Seq, Timestamp: p.Timestamp, PCM: pcm}); err {
	case nil:
	case ErrDuplicate:
		c.st.DuplicateDrop++
	case ErrConflict:
		c.st.ConflictDrop++
	case ErrFull:
		// Evict frames past their playout deadline first; only if the
		// queue is still full is the incoming frame an overflow drop.
		c.st.LateDrop += int64(c.q.DropBefore(c.nextSeq))
		if err := c.q.Insert(Frame{Seq: p.Seq, Timestamp: p.Timestamp, PCM: pcm}); err != nil {
			c.st.OverflowDrop++
		}
	}
	c.maybeAdjust(nowMs)
	c.maybeStart(nowMs)
}

// Tick advances the playout clock to nowMs, emitting due frames.
func (c *Controller) Tick(nowMs int64) {
	c.checkClock(nowMs)
	if !c.started {
		c.maybeStart(nowMs)
		return
	}
	for c.nextPlayMs <= nowMs {
		c.playOne()
		c.nextPlayMs += 10
	}
	// Long silence: drop back to prefill so playback re-buffers cleanly.
	if c.q.Len() == 0 && nowMs-c.lastPacketMs > c.cfg.SilenceMs {
		c.started = false
		c.prefillDeadline = nowMs + c.cfg.PrefillTimeoutMs
	}
}

// Tail reports whether the stream appears finished: playout is running,
// the queue is drained and no packet has arrived for a while. Drivers use
// it to stop without padding the tail with concealment.
func (c *Controller) Tail(nowMs int64) bool {
	return c.haveSeq && c.q.Len() == 0 && nowMs-c.lastPacketMs > c.cfg.SilenceMs
}

func (c *Controller) playOne() {
	if f, ok := c.q.Peek(); ok && f.Seq == c.nextSeq {
		c.q.Pop()
		if jump, resumed := c.plc.Real(f.PCM); resumed {
			c.st.NoteJump(jump)
		}
		c.st.NoteReal()
	} else {
		c.plc.Conceal(c.scratch[:])
		c.st.NetworkLost++
		c.st.NotePLC()
	}
	c.nextSeq++
	c.st.Observe(c.nextPlayMs, c.q.Len())
}

func (c *Controller) maybeStart(nowMs int64) {
	if c.started || !c.haveSeq {
		return
	}
	if c.q.Len() >= c.target || (c.q.Len() > 0 && nowMs >= c.prefillDeadline) {
		c.started = true
		c.nextPlayMs = c.anchorMs + int64(c.target)*10
	}
}

// maybeAdjust moves the target one frame per AdjustIntervalMs towards the
// desired depth, and only when it differs by at least two frames
// (hysteresis), so the buffer does not oscillate.
func (c *Controller) maybeAdjust(nowMs int64) {
	if nowMs-c.lastAdjustMs < c.cfg.AdjustIntervalMs {
		return
	}
	c.lastAdjustMs = nowMs
	desired := c.cfg.MinTarget + int(math.Ceil(c.jitterEst/5.0))
	if desired < c.cfg.MinTarget {
		desired = c.cfg.MinTarget
	}
	if desired > c.cfg.MaxTarget {
		desired = c.cfg.MaxTarget
	}
	switch {
	case desired >= c.target+2 && c.target < c.cfg.MaxTarget:
		c.target++
		// Growing the buffer delays playout by one frame slot.
		if c.started {
			c.nextPlayMs += 10
		}
	case desired <= c.target-2 && c.target > c.cfg.MinTarget:
		c.target--
	}
}

// checkClock re-anchors the playout schedule after a clock jump.
func (c *Controller) checkClock(nowMs int64) {
	if c.lastNowMs != 0 {
		backwards := nowMs < c.lastNowMs
		forward := nowMs-c.lastNowMs > c.cfg.ClockJumpMs
		if backwards || forward {
			if c.started {
				c.nextPlayMs = nowMs
			}
			c.prefillDeadline = nowMs + c.cfg.PrefillTimeoutMs
		}
	}
	c.lastNowMs = nowMs
}
