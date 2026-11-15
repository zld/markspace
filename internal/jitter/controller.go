// Package jitter implements the adaptive jitter buffer controller and
// the bounded sequence window shared by all receive paths.
package jitter

// Config tunes the adaptive buffer controller. Levels are in frames.
type Config struct {
	FrameMS  int64 // frame duration in milliseconds
	Target   int   // base target level in frames
	MinLevel int   // lower clamp for the adapted target
	MaxLevel int   // upper clamp for the adapted target
}

// Controller estimates arrival jitter (RFC 3550 style) and smooths the
// target buffer level between fixed bounds. Clock regressions, delay
// spikes and idle gaps are clamped so the level never goes negative or
// grows without bound.
type Controller struct {
	cfg         Config
	jitter      float64 // ms
	target      float64 // frames, smoothed
	lastTransit float64 // ms
	hasLast     bool
}

// NewController clamps the config into a sane range.
func NewController(cfg Config) *Controller {
	if cfg.FrameMS <= 0 {
		cfg.FrameMS = 20
	}
	if cfg.MinLevel < 1 {
		cfg.MinLevel = 1
	}
	if cfg.MaxLevel < cfg.MinLevel {
		cfg.MaxLevel = cfg.MinLevel
	}
	c := &Controller{cfg: cfg, target: float64(cfg.Target)}
	if c.target < float64(cfg.MinLevel) {
		c.target = float64(cfg.MinLevel)
	}
	if c.target > float64(cfg.MaxLevel) {
		c.target = float64(cfg.MaxLevel)
	}
	return c
}

// maxSpikeMS bounds how much a single arrival can move the estimate,
// absorbing idle gaps and wall-clock steps.
const maxSpikeMS = 1000.0

// Observe feeds one packet arrival. arrivalMs is the local receive
// time; sendMs is the packet timestamp on the frame clock.
func (c *Controller) Observe(arrivalMs int64, sendMs uint32) {
	transit := float64(arrivalMs) - float64(sendMs)
	if c.hasLast {
		d := transit - c.lastTransit
		if d < 0 { // clock regression or early arrival
			d = -d
		}
		if d > maxSpikeMS {
			d = maxSpikeMS
		}
		c.jitter += (d - c.jitter) / 16
		if c.jitter < 0 {
			c.jitter = 0
		}
	}
	c.lastTransit = transit
	c.hasLast = true

	desired := float64(c.cfg.Target) + 2*c.jitter/float64(c.cfg.FrameMS)
	c.target += (desired - c.target) / 8
	if c.target < float64(c.cfg.MinLevel) {
		c.target = float64(c.cfg.MinLevel)
	}
	if c.target > float64(c.cfg.MaxLevel) {
		c.target = float64(c.cfg.MaxLevel)
	}
}

// TargetLevel is the current adapted buffer target in frames.
func (c *Controller) TargetLevel() int {
	t := int(c.target + 0.5)
	if t < c.cfg.MinLevel {
		return c.cfg.MinLevel
	}
	if t > c.cfg.MaxLevel {
		return c.cfg.MaxLevel
	}
	return t
}

// JitterMS returns the current jitter estimate in milliseconds.
func (c *Controller) JitterMS() float64 { return c.jitter }
