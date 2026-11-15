// Package conceal implements packet loss concealment for fixed-size PCM
// frames: hold the last good frame, attenuate it across the loss run, switch
// to marked silence past a threshold, and crossfade the first recovered frame.
package conceal

import (
	"math"

	"jbbench/internal/protocol"
)

// Kind labels every frame leaving the concealer.
type Kind string

const (
	KindReal    Kind = "real"
	KindHold    Kind = "hold"
	KindAtten   Kind = "attenuated"
	KindSilence Kind = "silence"
	KindRecover Kind = "recovered"
)

// Frame is one output frame produced by the receive pipeline.
type Frame struct {
	Samples []int16
	Kind    Kind
	LossRun int     // consecutive lost frames at production time
	Energy  float64 // mean absolute sample amplitude
}

// Config parameterizes concealment behavior.
type Config struct {
	SilenceThreshold int // losses longer than this become marked silence
	CrossfadeSamples int // samples blended on the first recovered frame
}

// DefaultConfig returns the standard thresholds.
func DefaultConfig() Config {
	return Config{
		SilenceThreshold: 6, // 120 ms at 20 ms frames
		CrossfadeSamples: protocol.SamplesPerFrame / 4,
	}
}

// Stats summarizes concealment activity. Concealed frames never count as
// network-received packets; the receive layer owns that counter.
type Stats struct {
	ConcealedFrames  uint64
	MaxConcealRun    int
	RecoveryEpisodes uint64
	SmoothRecoveries uint64
	MaxRecoveryJump  int
	SilenceFrames    uint64
}

// Concealer is stateful and driven once per played frame.
type Concealer struct {
	cfg      Config
	lastGood []int16
	run      int
	stats    Stats
}

// New validates the configuration.
func New(cfg Config) *Concealer {
	if cfg.SilenceThreshold < 1 {
		cfg.SilenceThreshold = DefaultConfig().SilenceThreshold
	}
	if cfg.CrossfadeSamples < 0 || cfg.CrossfadeSamples > protocol.SamplesPerFrame {
		cfg.CrossfadeSamples = protocol.SamplesPerFrame / 4
	}
	return &Concealer{cfg: cfg}
}

// Energy returns the mean absolute amplitude of a frame.
func Energy(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += math.Abs(float64(v))
	}
	return sum / float64(len(s))
}

// Missing produces a substitute frame for one lost frame.
func (c *Concealer) Missing() Frame {
	c.run++
	if c.run > c.stats.MaxConcealRun {
		c.stats.MaxConcealRun = c.run
	}
	out := make([]int16, protocol.SamplesPerFrame)
	kind := KindSilence
	if c.lastGood != nil && c.run <= c.cfg.SilenceThreshold {
		// Attenuation halves every lost frame.
		gain := math.Pow(0.5, float64(c.run))
		for i, v := range c.lastGood {
			out[i] = int16(math.Round(float64(v) * gain))
		}
		if c.run == 1 {
			kind = KindHold
		} else {
			kind = KindAtten
		}
	} else {
		c.stats.SilenceFrames++
	}
	c.stats.ConcealedFrames++
	return Frame{Samples: out, Kind: kind, LossRun: c.run, Energy: Energy(out)}
}

// Real accepts a genuinely received frame and smooths the transition when it
// follows a loss run.
func (c *Concealer) Real(samples []int16) Frame {
	out := append([]int16(nil), samples...)
	kind := KindReal
	if c.run > 0 {
		c.stats.RecoveryEpisodes++
		c.applyCrossfade(out)
		kind = KindRecover
		if c.lastGood != nil {
			jump := maxAbsDelta(out, c.lastGood)
			if jump > c.stats.MaxRecoveryJump {
				c.stats.MaxRecoveryJump = jump
			}
			// A smooth recovery stays within roughly 10% of full-scale.
			if jump <= 3277 {
				c.stats.SmoothRecoveries++
			}
		}
	}
	c.run = 0
	c.lastGood = append([]int16(nil), samples...)
	return Frame{Samples: out, Kind: kind, LossRun: 0, Energy: Energy(out)}
}

func (c *Concealer) applyCrossfade(out []int16) {
	if c.lastGood == nil || c.cfg.CrossfadeSamples == 0 {
		return
	}
	n := c.cfg.CrossfadeSamples
	for i := 0; i < n; i++ {
		wIn := float64(i+1) / float64(n+1)
		out[i] = int16(math.Round(
			wIn*float64(out[i]) + (1-wIn)*float64(c.lastGood[i])))
	}
}

// RunLength is the current consecutive-loss count.
func (c *Concealer) RunLength() int { return c.run }

// Stats returns a copy of the concealment counters.
func (c *Concealer) Stats() Stats { return c.stats }

func maxAbsDelta(a, b []int16) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	worst := 0
	for i := 0; i < n; i++ {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	return worst
}
