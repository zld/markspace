package clock

import (
	"math"
)

// MaxSingleCorrectionNanos bounds every individual clock correction to 2 ms,
// as required by the kernel's soft-synchronisation policy.
const MaxSingleCorrectionNanos int64 = 2_000_000

// RecoveryReason classifies what the estimator did when an observation broke
// the steady linear model.
type RecoveryReason uint8

const (
	RecoveryNone       RecoveryReason = iota // model still valid
	RecoveryWindowFull                       // window rotated, no anomaly
	RecoveryJump                             // residual exceeded the threshold
	RecoveryClockReset                       // sender reported a clock reset
)

func (r RecoveryReason) String() string {
	switch r {
	case RecoveryNone:
		return "none"
	case RecoveryWindowFull:
		return "window_rotated"
	case RecoveryJump:
		return "jump"
	case RecoveryClockReset:
		return "clock_reset"
	default:
		return "unknown"
	}
}

type point struct {
	x, y float64
}

// DriftEstimator fits y = a + b*x over a sliding window of paired,
// time-correlated events (reference time vs. observed track time). It exposes
// the slope in ppm and hands out corrections clamped to 2 ms each.
type DriftEstimator struct {
	window       []point
	capacity     int
	jumpNanos    int64
	baseX, baseY int64 // baseline rebuilt after every reset/jump
	intercept    float64
	slope        float64 // observed units per reference unit, around 1.0
	ready        bool
	resets       uint64
	jumps        uint64
}

// NewDriftEstimator builds an estimator holding at most windowSize paired
// samples; jumpNanos is the residual magnitude (in the same units as the
// inputs, normally nanoseconds) that declares a discontinuity.
func NewDriftEstimator(windowSize int, jumpNanos int64) *DriftEstimator {
	if windowSize < 4 {
		windowSize = 4
	}
	if jumpNanos <= 0 {
		jumpNanos = 100_000_000
	}
	return &DriftEstimator{capacity: windowSize, jumpNanos: jumpNanos}
}

func clampCorrection(v int64) int64 {
	if v > MaxSingleCorrectionNanos {
		return MaxSingleCorrectionNanos
	}
	if v < -MaxSingleCorrectionNanos {
		return -MaxSingleCorrectionNanos
	}
	return v
}

// ObserveResult is the outcome of one paired observation.
type ObserveResult struct {
	CorrectionNanos int64
	DriftPPM        float64
	Reason          RecoveryReason
	ResidualNanos   int64
}

// Observe adds a paired sample (reference time x, observed time y, both
// nanoseconds). resetInput forces a baseline rebuild and is used when the
// unwrapper reported ReasonReset/ReasonJump.
func (d *DriftEstimator) Observe(x, y int64, resetInput bool) ObserveResult {
	if !d.ready {
		d.ready = true
		d.baseX, d.baseY = x, y
		d.window = append(d.window, point{0, 0})
		return ObserveResult{Reason: RecoveryNone}
	}

	// Warm-up: with a single baseline point no slope exists yet, so the
	// second observation seeds the fit without anomaly detection.
	if len(d.window) == 1 {
		d.window = append(d.window, point{float64(x - d.baseX), float64(y - d.baseY)})
		d.fit()
		return ObserveResult{Reason: RecoveryNone, DriftPPM: d.DriftPPM()}
	}

	reason := RecoveryNone
	if resetInput {
		reason = RecoveryClockReset
	} else {
		pred := d.predict(x)
		residual := y - pred
		if abs64(residual) > d.jumpNanos {
			reason = RecoveryJump
		}
	}

	if reason != RecoveryNone {
		// Rebuild the baseline: keep the reference origin, restart the fit.
		d.baseX, d.baseY = x, y
		d.window = d.window[:0]
		d.window = append(d.window, point{0, 0})
		d.intercept, d.slope, d.ready = 0, 0, true
		d.resets++
		if reason == RecoveryJump {
			d.jumps++
		}
		return ObserveResult{Reason: reason, ResidualNanos: 0, CorrectionNanos: 0}
	}

	rotated := len(d.window) >= d.capacity
	if len(d.window) >= d.capacity {
		d.window = d.window[1:]
	}
	d.window = append(d.window, point{float64(x - d.baseX), float64(y - d.baseY)})
	d.fit()
	if rotated {
		reason = RecoveryWindowFull
	}

	corr := clampCorrection(d.predict(x) - y)
	return ObserveResult{
		CorrectionNanos: corr,
		DriftPPM:        d.DriftPPM(),
		Reason:          reason,
		ResidualNanos:   y - d.predict(x),
	}
}

func (d *DriftEstimator) predict(x int64) int64 {
	if !d.ready {
		return x
	}
	dx := float64(x - d.baseX)
	return d.baseY + int64(math.Round(d.intercept+d.slope*dx))
}

func (d *DriftEstimator) fit() {
	n := float64(len(d.window))
	if n < 2 {
		d.intercept, d.slope = 0, 0
		return
	}
	var sx, sy, sxx, sxy float64
	for _, p := range d.window {
		sx += p.x
		sy += p.y
		sxx += p.x * p.x
		sxy += p.x * p.y
	}
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-9 {
		d.intercept, d.slope = 0, 0
		return
	}
	d.slope = (n*sxy - sx*sy) / den
	d.intercept = (sy - d.slope*sx) / n
}

// DriftPPM returns the fitted slope expressed as parts per million; positive
// means the observed clock runs faster than the reference.
func (d *DriftEstimator) DriftPPM() float64 {
	return (d.slope - 1.0) * 1e6
}

// Ready reports whether a slope has been fitted from at least two samples.
func (d *DriftEstimator) Ready() bool { return d.ready && len(d.window) >= 2 }

// Jumps returns the number of jump-induced rebuilds.
func (d *DriftEstimator) Jumps() uint64 { return d.jumps }

// Resets returns the total number of baseline rebuilds.
func (d *DriftEstimator) Resets() uint64 { return d.resets }

// Clamp applies the 2 ms clamp to an externally computed correction.
func Clamp(correctionNanos int64) int64 { return clampCorrection(correctionNanos) }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// DriftState is the serialisable estimator checkpoint.
type DriftState struct {
	Window    []point `json:"window"`
	Capacity  int     `json:"capacity"`
	JumpNanos int64   `json:"jump_nanos"`
	BaseX     int64   `json:"base_x"`
	BaseY     int64   `json:"base_y"`
	Intercept float64 `json:"intercept"`
	Slope     float64 `json:"slope"`
	Ready     bool    `json:"ready"`
	Resets    uint64  `json:"resets"`
	Jumps     uint64  `json:"jumps"`
}

// Snapshot captures estimator state for checkpointing.
func (d *DriftEstimator) Snapshot() DriftState {
	w := make([]point, len(d.window))
	copy(w, d.window)
	return DriftState{
		Window: w, Capacity: d.capacity, JumpNanos: d.jumpNanos,
		BaseX: d.baseX, BaseY: d.baseY, Intercept: d.intercept,
		Slope: d.slope, Ready: d.ready, Resets: d.resets, Jumps: d.jumps,
	}
}

// RestoreDrift rebuilds an estimator from a checkpoint.
func RestoreDrift(s DriftState) *DriftEstimator {
	d := NewDriftEstimator(s.Capacity, s.JumpNanos)
	d.window = append(d.window, s.Window...)
	d.baseX, d.baseY = s.BaseX, s.BaseY
	d.intercept, d.slope = s.Intercept, s.Slope
	d.ready, d.resets, d.jumps = s.Ready, s.Resets, s.Jumps
	return d
}
