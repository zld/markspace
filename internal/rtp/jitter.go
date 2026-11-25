package rtp

import "time"

// JitterConfig bounds the adaptive playout buffer. Target depth moves with
// the observed bandwidth but is clamped to [MinTarget, MaxTarget] packets.
type JitterConfig struct {
	MinTarget    int           `json:"min_target"`
	MaxTarget    int           `json:"max_target"`
	StartTarget  int           `json:"start_target"`
	// BandwidthFloor / BandwidthCeil bracket the usable sample range in
	// bytes/second. Samples at the floor map to MaxTarget, at the ceil to
	// MinTarget; outside the bracket they clamp.
	BandwidthFloor int64       `json:"bandwidth_floor"`
	BandwidthCeil  int64       `json:"bandwidth_ceil"`
	// EMAAlpha is the exponential moving average weight in percent (1..99)
	// applied to each new bandwidth sample.
	EMAAlpha       int         `json:"ema_alpha"`
}

// DefaultJitterConfig returns conservative offline bounds.
func DefaultJitterConfig() JitterConfig {
	return JitterConfig{
		MinTarget: 4, MaxTarget: 64, StartTarget: 16,
		BandwidthFloor: 64_000, BandwidthCeil: 8_000_000, EMAAlpha: 25,
	}
}

// BandwidthSample is one bandwidth observation with its arrival time.
type BandwidthSample struct {
	T time.Time
	// BytesPerSecond is the measured available bandwidth.
	BytesPerSecond int64
}

// JitterBuffer tracks the adaptive target depth and live occupancy. The
// reorder buffer feeds occupancy; bandwidth samples drive the target.
type JitterBuffer struct {
	cfg      JitterConfig
	ema      float64
	emaInit  bool
	target   int
	level    int
	MinLevel int `json:"min_level"`
	MaxLevel int `json:"max_level"`
	// Samples records every clamped target decision for the summary.
	Samples  []TargetSample `json:"samples"`
}

// TargetSample is one adaptive decision with the inputs that produced it.
type TargetSample struct {
	ObservedBPS int64 `json:"observed_bps"`
	SmoothedBPS int64 `json:"smoothed_bps"`
	Target      int   `json:"target"`
	ClampedLow  bool  `json:"clamped_low"`
	ClampedHigh bool  `json:"clamped_high"`
}

// NewJitterBuffer validates the config (falling back to defaults on bad
// bounds) and seeds the target at StartTarget.
func NewJitterBuffer(cfg JitterConfig) *JitterBuffer {
	d := DefaultJitterConfig()
	if cfg.MinTarget <= 0 {
		cfg.MinTarget = d.MinTarget
	}
	if cfg.MaxTarget < cfg.MinTarget {
		cfg.MinTarget, cfg.MaxTarget = d.MinTarget, d.MaxTarget
	}
	if cfg.StartTarget < cfg.MinTarget || cfg.StartTarget > cfg.MaxTarget {
		cfg.StartTarget = d.StartTarget
	}
	if cfg.BandwidthCeil <= cfg.BandwidthFloor {
		cfg.BandwidthFloor, cfg.BandwidthCeil = d.BandwidthFloor, d.BandwidthCeil
	}
	if cfg.EMAAlpha <= 0 || cfg.EMAAlpha > 99 {
		cfg.EMAAlpha = d.EMAAlpha
	}
	return &JitterBuffer{cfg: cfg, target: cfg.StartTarget, MinLevel: 1 << 30}
}

// Observe folds one bandwidth sample into the EMA and recomputes the clamped
// target. Lower bandwidth yields a deeper buffer and vice versa.
func (j *JitterBuffer) Observe(s BandwidthSample) TargetSample {
	a := float64(j.cfg.EMAAlpha) / 100.0
	if !j.emaInit {
		j.ema = float64(s.BytesPerSecond)
		j.emaInit = true
	} else {
		j.ema = a*float64(s.BytesPerSecond) + (1-a)*j.ema
	}
	raw := j.targetFor(j.ema)
	ts := TargetSample{ObservedBPS: s.BytesPerSecond, SmoothedBPS: int64(j.ema), Target: raw.Target,
		ClampedLow: raw.ClampsLow, ClampedHigh: raw.ClampsHigh}
	j.target = ts.Target
	j.Samples = append(j.Samples, ts)
	return ts
}

type targetResult struct {
	Target     int
	ClampsLow  bool
	ClampsHigh bool
}

func (j *JitterBuffer) targetFor(bps float64) targetResult {
	lo, hi := float64(j.cfg.BandwidthFloor), float64(j.cfg.BandwidthCeil)
	clamped := bps
	clampLow, clampHigh := false, false
	if clamped <= lo {
		clamped, clampLow = lo, true
	}
	if clamped >= hi {
		clamped, clampHigh = hi, true
	}
	// Fraction 0 at the floor (need deepest buffer) -> 1 at the ceiling.
	frac := (clamped - lo) / (hi - lo)
	span := float64(j.cfg.MaxTarget - j.cfg.MinTarget)
	t := j.cfg.MaxTarget - int(span*frac+0.5)
	if t < j.cfg.MinTarget {
		t = j.cfg.MinTarget
	}
	if t > j.cfg.MaxTarget {
		t = j.cfg.MaxTarget
	}
	return targetResult{Target: t, ClampsLow: clampLow, ClampsHigh: clampHigh}
}

// SetLevel records the current reorder-buffer occupancy and updates extrema.
func (j *JitterBuffer) SetLevel(n int) {
	j.level = n
	if n < j.MinLevel {
		j.MinLevel = n
	}
	if n > j.MaxLevel {
		j.MaxLevel = n
	}
}

// Target returns the current adaptive target depth in packets.
func (j *JitterBuffer) Target() int { return j.target }

// Level returns the most recently recorded occupancy.
func (j *JitterBuffer) Level() int { return j.level }

// Underflow reports whether the live level is below the adaptive target.
func (j *JitterBuffer) Underflow() bool { return j.level < j.target }

// SmoothedBandwidth returns the EMA bandwidth in bytes/second, or 0 before
// any sample has been observed.
func (j *JitterBuffer) SmoothedBandwidth() int64 {
	if !j.emaInit {
		return 0
	}
	return int64(j.ema)
}
