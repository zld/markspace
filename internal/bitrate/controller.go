// Package bitrate turns link bandwidth samples into a bounded, slew-limited
// target bitrate and per-chunk byte budgets. Every degradation path (zero
// bandwidth, sample interruption, timeout) produces a deterministic result so
// incident replay always reaches the same decisions.
package bitrate

import (
	"math"

	"github.com/medialab/medialab/internal/clock"
)

// Sample is one observed link measurement on the shared tick timeline.
type Sample struct {
	AtTick      int64
	BandwidthBps int64 // bytes per second currently available on the link
}

// Config fixes the controller's deterministic parameters.
type Config struct {
	TargetBps        int64 // configured service-level target
	MinBps           int64 // floor the controller never decides below
	SlewUpPct        float64
	SlewDownPct      float64
	EWMAAlpha        float64 // weight of the newest sample, 0 < a <= 1
	InterruptTicks   int64   // no samples for this long -> degraded budget
	TimeoutTicks     int64   // no samples for this long -> minimum budget
	ChunkDurationMs  int64
}

// State is a point-in-time snapshot for the report.
type State struct {
	AtTick      int64
	TargetBps   int64 // slew-limited decision
	ActualBps   int64 // raw latest sample (0 when unavailable)
	EstimateBps int64 // smoothed estimate driving the decision
	Degraded    bool
	TimedOut    bool
}

// Controller is a single-stream, allocation-free bandwidth controller.
type Controller struct {
	cfg      Config
	est      float64
	current  int64
	last     Sample
	have     bool
	zeroSeen bool
}

// NewController validates the configuration and seeds the initial decision at
// the configured target (clipped to the floor).
func NewController(cfg Config) (*Controller, error) {
	if cfg.TargetBps <= 0 {
		return nil, clock.NewError(clock.KindOutOfRange, "bitrate", "", 0, "target must be positive")
	}
	if cfg.MinBps < 0 {
		return nil, clock.NewError(clock.KindNegativeTime, "bitrate", "", 0, "min rate cannot be negative")
	}
	if cfg.EWMAAlpha <= 0 || cfg.EWMAAlpha > 1 {
		return nil, clock.NewError(clock.KindOutOfRange, "bitrate", "", 0, "EWMA alpha must be in (0,1]")
	}
	if cfg.SlewUpPct < 0 || cfg.SlewDownPct < 0 || cfg.ChunkDurationMs <= 0 {
		return nil, clock.NewError(clock.KindOutOfRange, "bitrate", "", 0, "invalid slew or chunk parameters")
	}
	seed := cfg.TargetBps
	if seed < cfg.MinBps {
		seed = cfg.MinBps
	}
	return &Controller{cfg: cfg, est: float64(seed), current: seed}, nil
}

// Observe folds one bandwidth sample into the estimate. Negative readings are
// rejected; an explicit zero is honored (not treated as missing data).
func (c *Controller) Observe(s Sample) error {
	if s.BandwidthBps < 0 {
		return clock.NewError(clock.KindNegativeTime, "bitrate", "", s.AtTick,
			"bandwidth cannot be negative")
	}
	if c.have && s.AtTick < c.last.AtTick {
		return clock.NewError(clock.KindNonMonotonic, "bitrate", "", s.AtTick,
			"bandwidth samples must be monotonic in time")
	}
	if !c.have {
		c.est = float64(s.BandwidthBps)
	} else {
		c.est = c.cfg.EWMAAlpha*float64(s.BandwidthBps) + (1-c.cfg.EWMAAlpha)*c.est
	}
	c.last, c.have = s, true
	c.zeroSeen = s.BandwidthBps == 0
	return nil
}
// Decide applies the degradation ladder and slew limiter at time nowTick.
func (c *Controller) Decide(nowTick int64) State {
	st := State{AtTick: nowTick, ActualBps: c.actual(), TargetBps: c.current}
	switch {
	case !c.have:
		// No telemetry at all: keep the seeded target but mark degraded so
		// the report distinguishes "no data" from "measured healthy".
		st.EstimateBps, st.Degraded = c.current, true
	case c.zeroSeen && c.last.BandwidthBps == 0:
		// Explicit zero bandwidth is a hard, deterministic floor: no slew
		// smoothing and no ramp-up guess until a positive sample arrives.
		st.EstimateBps, st.Degraded = 0, true
		c.current, st.TargetBps = c.cfg.MinBps, c.cfg.MinBps
	case c.silence(nowTick) >= c.cfg.TimeoutTicks:
		// Timeout is also a hard floor: stale telemetry must not sustain a
		// high bitrate decision.
		st.EstimateBps = int64(math.Round(c.est))
		st.Degraded, st.TimedOut = true, true
		c.current, st.TargetBps = c.cfg.MinBps, c.cfg.MinBps
	case c.silence(nowTick) >= c.cfg.InterruptTicks:
		st.EstimateBps = int64(math.Round(c.est))
		st.Degraded = true
		st.TargetBps = c.slewToward(c.floorOfEstimate())
	default:
		st.EstimateBps = int64(math.Round(c.est))
		st.TargetBps = c.slewToward(c.floorOfEstimate())
	}
	c.current = st.TargetBps
	return st
}

// ChunkBudget converts the current decision into a byte budget for one chunk.
func (c *Controller) ChunkBudget() int64 {
	bps := c.current
	if bps < c.cfg.MinBps {
		bps = c.cfg.MinBps
	}
	return bps * c.cfg.ChunkDurationMs / clock.TicksPerSecond
}

// DeltaPercent reports (target-actual)/target as a whole-percentage delta for
// the summary; a missing actual yields 100 (fully below target).
func (s State) DeltaPercent() int {
	if s.ActualBps <= 0 {
		return 100
	}
	delta := float64(s.TargetBps-s.ActualBps) / float64(s.TargetBps) * 100
	return int(math.Round(delta))
}

func (c *Controller) actual() int64 {
	if !c.have {
		return 0
	}
	return c.last.BandwidthBps
}

func (c *Controller) silence(nowTick int64) int64 {
	if !c.have {
		return math.MaxInt64
	}
	d := nowTick - c.last.AtTick
	if d < 0 {
		return 0
	}
	return d
}

func (c *Controller) floorOfEstimate() int64 {
	want := int64(math.Round(c.est))
	if want < c.cfg.MinBps {
		return c.cfg.MinBps
	}
	if want > c.cfg.TargetBps {
		return c.cfg.TargetBps // never exceed the configured service target
	}
	return want
}

// slewToward moves current toward want by at most the configured per-decision
// percentage, with separate upward and downward limits.
func (c *Controller) slewToward(want int64) int64 {
	if want == c.current {
		return c.current
	}
	var limit float64
	if want > c.current {
		limit = float64(c.current) * c.cfg.SlewUpPct / 100
		next := c.current + int64(math.Ceil(limit))
		if next > want || limit == 0 {
			return want
		}
		return next
	}
	limit = float64(c.current) * c.cfg.SlewDownPct / 100
	next := c.current - int64(math.Ceil(limit))
	if next < want || limit == 0 {
		return want
	}
	if next < c.cfg.MinBps {
		next = c.cfg.MinBps
	}
	return next
}
