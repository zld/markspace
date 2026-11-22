package ingest

import (
	"math"
	"sort"

	"mediacore/media"
)

// BandwidthConfig configures the sliding-window bandwidth estimator.
type BandwidthConfig struct {
	// WindowSamples is how many recent delivery intervals the moving average
	// uses.
	WindowSamples int
	// DropRatio triggers sudden-drop handling when the smoothed estimate falls
	// below DropRatio of the peak estimate (e.g. 0.5 for a halving).
	DropRatio float64
	// InFlightCapMS bounds how much buffering the target permits. During a
	// sudden drop the effective cap is halved. Zero disables the cap.
	InFlightCapMS int
	// CeilingKBPS is an optional hard cap on the recommended target rate.
	CeilingKBPS int
	// FloorKBPS is an optional lower bound on the recommended target rate.
	FloorKBPS int
}

// DefaultBandwidthConfig returns a 20-interval window with drop detection.
func DefaultBandwidthConfig() BandwidthConfig {
	return BandwidthConfig{
		WindowSamples: 20,
		DropRatio:     0.5,
		InFlightCapMS: 250,
	}
}

// Validate reports configuration errors.
func (c BandwidthConfig) Validate() error {
	if c.WindowSamples <= 0 {
		return media.New(media.CatInvalidParams, media.StageIngest, "bandwidth window must be positive")
	}
	if c.DropRatio <= 0 || c.DropRatio >= 1 {
		return media.New(media.CatInvalidParams, media.StageIngest, "drop ratio must be in (0,1)")
	}
	if c.InFlightCapMS < 0 || c.CeilingKBPS < 0 || c.FloorKBPS < 0 {
		return media.New(media.CatInvalidParams, media.StageIngest, "bandwidth bounds cannot be negative")
	}
	return nil
}

type interval struct {
	dtMS  int64
	bytes int64
}

// BandwidthEstimator tracks a moving-average delivery rate and recommends a
// target bitrate that never exceeds the observed input ceiling. It is not safe
// for concurrent use.
type BandwidthEstimator struct {
	cfg      BandwidthConfig
	window   []interval
	lastTime int64
	pending  int64
	haveLast bool
	peakKBPS float64
	drop     bool
	sudden   int
}

// NewBandwidthEstimator constructs an estimator with clamped defaults.
func NewBandwidthEstimator(cfg BandwidthConfig) *BandwidthEstimator {
	def := DefaultBandwidthConfig()
	if cfg.WindowSamples <= 0 {
		cfg.WindowSamples = def.WindowSamples
	}
	if cfg.DropRatio <= 0 || cfg.DropRatio >= 1 {
		cfg.DropRatio = def.DropRatio
	}
	return &BandwidthEstimator{cfg: cfg}
}

// Observe records bytes delivered since the previous observation at timeMS.
// Out-of-order observation times are folded into the pending interval and
// negative byte counts are ignored. The first observation only starts the
// clock because no elapsed interval exists yet.
func (e *BandwidthEstimator) Observe(timeMS, bytes int64) {
	if bytes < 0 {
		return
	}
	if !e.haveLast {
		e.haveLast = true
		e.lastTime = timeMS
		return
	}
	if timeMS < e.lastTime {
		timeMS = e.lastTime
	}
	e.pending += bytes
	if timeMS == e.lastTime {
		return
	}
	e.window = append(e.window, interval{dtMS: timeMS - e.lastTime, bytes: e.pending})
	e.lastTime = timeMS
	e.pending = 0
	if len(e.window) > e.cfg.WindowSamples {
		e.window = e.window[len(e.window)-e.cfg.WindowSamples:]
	}
	e.refreshDrop()
}

func (e *BandwidthEstimator) refreshDrop() {
	est := e.estimate()
	if est <= 0 {
		return
	}
	if est > e.peakKBPS {
		e.peakKBPS = est
	}
	wasDrop := e.drop
	e.drop = e.peakKBPS > 0 && est < e.peakKBPS*e.cfg.DropRatio
	if e.drop && !wasDrop {
		e.sudden++
	}
}

func (e *BandwidthEstimator) estimate() float64 {
	var totalBytes, totalMS int64
	for _, s := range e.window {
		totalBytes += s.bytes
		totalMS += s.dtMS
	}
	if totalMS <= 0 {
		return 0
	}
	return float64(totalBytes) * 8 / float64(totalMS)
}

// EstimateKBPS is the sliding-window average rate in kilobits per second.
func (e *BandwidthEstimator) EstimateKBPS() float64 { return e.estimate() }

// InputCeilingKBPS is the 90th-percentile interval rate in the recent window.
// It is the hard input bound the target may never exceed, and the percentile
// keeps one burst from defining the ceiling.
func (e *BandwidthEstimator) InputCeilingKBPS() float64 {
	if len(e.window) == 0 {
		return 0
	}
	rates := make([]float64, 0, len(e.window))
	for _, s := range e.window {
		if s.dtMS <= 0 {
			continue
		}
		rates = append(rates, float64(s.bytes)*8/float64(s.dtMS))
	}
	if len(rates) == 0 {
		return 0
	}
	sort.Float64s(rates)
	idx := int(math.Ceil(float64(len(rates))*0.9)) - 1
	if idx < 0 {
		idx = 0
	}
	return rates[idx]
}

// DropDetected reports whether the latest estimate was a sudden drop.
func (e *BandwidthEstimator) DropDetected() bool { return e.drop }

// SuddenDrops counts detected drop transitions.
func (e *BandwidthEstimator) SuddenDrops() int { return e.sudden }

// InFlightLimitBytes is the maximum in-flight buffer in bytes implied by the
// current estimate and the configured delay cap.
func (e *BandwidthEstimator) InFlightLimitBytes(estKBPS float64) int64 {
	capMS := int64(e.cfg.InFlightCapMS)
	if capMS <= 0 {
		return 0
	}
	if e.drop {
		capMS /= 2
	}
	return int64(estKBPS * 1000 / 8 * float64(capMS) / 1000)
}

// TargetKBPS recommends a target bitrate bounded by the input ceiling and the
// configured floor/ceiling. The reason string explains which bound applied.
func (e *BandwidthEstimator) TargetKBPS(estKBPS, ceilingKBPS float64) (int, string) {
	target := estKBPS
	reason := "estimate"
	if e.drop {
		target *= e.cfg.DropRatio
		reason = "sudden_drop"
	}
	if ceilingKBPS > 0 && target > ceilingKBPS {
		target = ceilingKBPS
		reason = "input_ceiling"
	}
	if e.cfg.CeilingKBPS > 0 && target > float64(e.cfg.CeilingKBPS) {
		target = float64(e.cfg.CeilingKBPS)
		reason = "configured_ceiling"
	}
	if e.cfg.FloorKBPS > 0 && target < float64(e.cfg.FloorKBPS) {
		target = float64(e.cfg.FloorKBPS)
		reason = "configured_floor"
	}
	return int(math.Round(target)), reason
}

// BandwidthReport summarizes the estimator state for the manifest.
type BandwidthReport struct {
	EstimateKBPS     float64 `json:"estimate_kbps"`
	InputCeilingKBPS float64 `json:"input_ceiling_kbps"`
	TargetKBPS       int     `json:"target_kbps"`
	Reason           string  `json:"reason"`
	DropDetected     bool    `json:"drop_detected"`
	SuddenDrops      int     `json:"sudden_drops"`
	InFlightLimit    int64   `json:"inflight_limit_bytes"`
}

// Report snapshots the current estimator state.
func (e *BandwidthEstimator) Report() BandwidthReport {
	est := e.EstimateKBPS()
	ceil := e.InputCeilingKBPS()
	target, reason := e.TargetKBPS(est, ceil)
	return BandwidthReport{
		EstimateKBPS:     round2(est),
		InputCeilingKBPS: round2(ceil),
		TargetKBPS:       target,
		Reason:           reason,
		DropDetected:     e.drop,
		SuddenDrops:      e.sudden,
		InFlightLimit:    e.InFlightLimitBytes(est),
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
