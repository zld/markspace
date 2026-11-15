// Package jitter holds the shared jitter-buffer control strategy: the
// target-watermark controller, the reorder window and the Player facade that
// both the live UDP path and the offline replay path drive.
package jitter

import (
	"time"

	"jbbench/internal/protocol"
)

// ControllerConfig parameterizes the shared buffer controller.
type ControllerConfig struct {
	InitialLevel time.Duration // target watermark before enough samples arrive
	MinLevel     time.Duration // hard lower bound
	MaxLevel     time.Duration // hard upper bound
	Smooth       float64       // EWMA factor in (0,1]
	MaxStep      time.Duration // largest single-frame level adjustment
	Warmup       int           // arrival samples required before adapting
	Frame        time.Duration // nominal frame duration
}

// DefaultControllerConfig returns conservative voice-over-UDP defaults.
func DefaultControllerConfig() ControllerConfig {
	frame := protocol.FrameDurationNS()
	return ControllerConfig{
		InitialLevel: 3 * frame,
		MinLevel:     frame,
		MaxLevel:     20 * frame,
		Smooth:       0.125,
		MaxStep:      frame,
		Warmup:       30,
		Frame:        frame,
	}
}

// Controller estimates the target buffer watermark from arrival-interval
// deviation. It is safe for sequential use by a single receive path.
type Controller struct {
	cfg       ControllerConfig
	level     time.Duration
	jitter    time.Duration // EWMA of |arrival gap - frame|
	samples   int
	decisions int
	last      time.Duration
	hasLast   bool
}

// NewController validates and clamps the configuration.
func NewController(cfg ControllerConfig) *Controller {
	if cfg.Frame <= 0 {
		cfg.Frame = protocol.FrameDurationNS()
	}
	if cfg.MinLevel <= 0 {
		cfg.MinLevel = cfg.Frame
	}
	if cfg.MaxLevel < cfg.MinLevel {
		cfg.MaxLevel = cfg.MinLevel
	}
	if cfg.InitialLevel < cfg.MinLevel {
		cfg.InitialLevel = cfg.MinLevel
	}
	if cfg.InitialLevel > cfg.MaxLevel {
		cfg.InitialLevel = cfg.MaxLevel
	}
	if cfg.Smooth <= 0 || cfg.Smooth > 1 {
		cfg.Smooth = 0.125
	}
	if cfg.MaxStep <= 0 {
		cfg.MaxStep = cfg.Frame
	}
	if cfg.Warmup < 0 {
		cfg.Warmup = 0
	}
	return &Controller{cfg: cfg, level: cfg.InitialLevel}
}

// ObserveArrival feeds one packet arrival timestamp (relative clock).
func (c *Controller) ObserveArrival(arrival time.Duration) {
	if c.hasLast {
		gap := arrival - c.last
		if gap < 0 {
			gap = -gap
		}
		dev := gap - c.cfg.Frame
		if dev < 0 {
			dev = -dev
		}
		if c.samples == 0 {
			c.jitter = dev
		} else {
			c.jitter += time.Duration(c.cfg.Smooth * float64(dev-c.jitter))
		}
		c.samples++
		if c.samples >= c.cfg.Warmup {
			c.adapt()
		}
	}
	c.last = arrival
	c.hasLast = true
}

func (c *Controller) adapt() {
	// Two jitter units of margin on top of the initial watermark.
	want := c.cfg.InitialLevel + 2*c.jitter
	want = clampDur(want, c.cfg.MinLevel, c.cfg.MaxLevel)
	delta := clampDur(want-c.level, -c.cfg.MaxStep, c.cfg.MaxStep)
	c.level = clampDur(c.level+delta, c.cfg.MinLevel, c.cfg.MaxLevel)
	c.decisions++
}

// Level returns the current target watermark.
func (c *Controller) Level() time.Duration { return c.level }

// LevelFrames expresses the watermark in whole frames (rounded up).
func (c *Controller) LevelFrames() int {
	return int((c.level + c.cfg.Frame - 1) / c.cfg.Frame)
}

// Jitter returns the smoothed arrival-interval deviation.
func (c *Controller) Jitter() time.Duration { return c.jitter }

// Samples returns the number of arrival-gap observations.
func (c *Controller) Samples() int { return c.samples }

// Decisions returns how many times the watermark was re-evaluated.
func (c *Controller) Decisions() int { return c.decisions }

// Config returns the effective configuration.
func (c *Controller) Config() ControllerConfig { return c.cfg }

func clampDur(v, lo, hi time.Duration) time.Duration {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
