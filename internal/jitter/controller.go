// Package jitter implements the shared playout buffer controller used
// identically by the UDP and TCP receive paths.
package jitter

import (
	"sync"
	"time"
)

// Config tunes a Controller.
type Config struct {
	MinTarget   int // minimum target waterline, frames
	MaxTarget   int // maximum target waterline, frames (hard capacity)
	InitTarget  int // initial target waterline, frames
	FramePeriod time.Duration
	// ExpandStep: frames added to the target per severe jitter event.
	ExpandStep int
	// ShrinkInterval: target decays by one frame at most this often.
	ShrinkInterval time.Duration
	// JitterHigh: inter-arrival deviation (frames) that forces expansion.
	JitterHigh float64
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.MinTarget < 1 {
		out.MinTarget = 1
	}
	if out.MaxTarget < out.MinTarget {
		out.MaxTarget = out.MinTarget
	}
	if out.InitTarget < out.MinTarget {
			out.InitTarget = out.MinTarget
	}
	if out.InitTarget > out.MaxTarget {
		out.InitTarget = out.MaxTarget
	}
	if out.ExpandStep < 1 {
		out.ExpandStep = 2
	}
	if out.ShrinkInterval <= 0 {
		out.ShrinkInterval = 2 * time.Second
	}
	if out.JitterHigh <= 0 {
		out.JitterHigh = 1.5
	}
	return out
}

// Stats counts classified packet and frame events.
type Stats struct {
	Received   uint64
	Duplicate  uint64
	Late       uint64
	Overflow   uint64
	Malformed  uint64
	Played     uint64
	Concealed  uint64
	Evicted    uint64
}

// Controller is the shared playout buffer. Network goroutines call
// Add; the playout clock calls Tick; the sampler calls Snapshot.
// All methods are safe for concurrent use.
type Controller struct {
	cfg Config

	mu         sync.Mutex
	win        *Window
	target     int
	jitterEMA  float64 // inter-arrival deviation, in frame periods
	lastArrival time.Time
	haveArrival bool
	lastShrink  time.Time
	stats      Stats
	// now is injectable for tests.
	now func() time.Time
}

func NewController(cfg Config) *Controller {
	c := cfg.withDefaults()
	return &Controller{
		cfg: c,
		win: NewWindow(c.MaxTarget),
		target: c.InitTarget,
		now: time.Now,
	}
}

// Add ingests one validated frame from the network path.
func (c *Controller) Add(f Frame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.stats.Received++
	c.updateTargetLocked(now, f)
	switch c.win.Insert(f) {
	case Duplicate:
		c.stats.Duplicate++
	case TooLate:
		c.stats.Late++
	case Overflow:
		c.stats.Overflow++
	}
}

// NoteMalformed counts a packet that failed validation before Add.
func (c *Controller) NoteMalformed() {
	c.mu.Lock()
	c.stats.Malformed++
	c.mu.Unlock()
}

// updateTargetLocked adapts the target waterline: expand fast on
// burst jitter, shrink slowly when stable.
func (c *Controller) updateTargetLocked(now time.Time, f Frame) {
	if c.haveArrival {
		dt := now.Sub(c.lastArrival).Seconds() / c.cfg.FramePeriod.Seconds()
		dev := dt - 1.0
		if dev < 0 {
			dev = -dev
		}
		const alpha = 0.25
		c.jitterEMA += alpha * (dev - c.jitterEMA)
		if c.jitterEMA > c.cfg.JitterHigh {
			// Fast expansion, bounded by capacity.
			nt := c.target + c.cfg.ExpandStep
			if nt > c.cfg.MaxTarget {
				nt = c.cfg.MaxTarget
			}
			c.target = nt
			c.lastShrink = now
		}
	}
	c.lastArrival = now
	c.haveArrival = true
	// Slow decay while stable.
	if c.target > c.cfg.MinTarget && now.Sub(c.lastShrink) >= c.cfg.ShrinkInterval &&
		c.jitterEMA < c.cfg.JitterHigh/2 {
		c.target--
		c.lastShrink = now
		// Shed frames that no longer fit the smaller target.
		c.stats.Evicted += uint64(c.win.EvictBeyond(c.target))
	}
}

// Tick is called by the playout clock once per frame period. It never
// blocks on the network: it returns the next queued frame, or ok=false
// when the buffer misses and the caller must conceal.
func (c *Controller) Tick() (Frame, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.win.PopNext()
	if ok {
		c.stats.Played++
	} else {
		c.stats.Concealed++
	}
	return f, ok
}

// Snapshot is a consistent point-in-time view for the waterline sampler.
type Snapshot struct {
	Queued int
	Target int
}

func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{Queued: c.win.Len(), Target: c.target}
}

// Stats returns a copy of the counters.
func (c *Controller) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
