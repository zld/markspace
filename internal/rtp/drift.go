package rtp

import (
	"math"

	"mediatool/internal/timeline"
)

// Observation pairs one wall-clock reading (arrival time, milliseconds)
// with the unwrapped media-clock reading at that instant (clock units).
type Observation struct {
	WallMS    int64
	MediaTime uint64
}

// DriftResult is the estimator output. PPM is parts-per-million relative
// drift: positive means the device clock runs faster than the wall clock.
type DriftResult struct {
	// PPM is (slope - nominalSlope) / nominalSlope * 1e6.
	PPM float64
	// Correction is the cumulative media-clock correction to apply to
	// the latest observation, in media-clock units.
	Correction int64
	// Confidence qualifies the estimate from the residual spread.
	Confidence timeline.Confidence
	// Samples is the number of observations consumed.
	Samples int
}

// DriftEstimator fits media time against wall time with ordinary least
// squares over a bounded sliding window, so memory does not grow with the
// number of fragments.
type DriftEstimator struct {
	nominalSlope float64 // media units per millisecond
	window       int
	obs          []Observation
}

// NewDriftEstimator builds an estimator for a clock running at clockRate
// units per second (e.g. 90000 for a 90 kHz RTP clock). window bounds the
// memory used by the regression.
func NewDriftEstimator(clockRate uint32, window int) *DriftEstimator {
	if window < 2 {
		window = 16
	}
	return &DriftEstimator{
		nominalSlope: float64(clockRate) / 1000.0,
		window:       window,
	}
}

// Add records one observation, evicting the oldest beyond the window.
func (e *DriftEstimator) Add(o Observation) {
	e.obs = append(e.obs, o)
	if len(e.obs) > e.window {
		e.obs = e.obs[len(e.obs)-e.window:]
	}
}

// Estimate computes drift, the correction for the newest observation and
// a confidence level derived from the normalized residual spread. With
// fewer than two observations the correction is zero and confidence none.
func (e *DriftEstimator) Estimate() DriftResult {
	n := len(e.obs)
	if n < 2 {
		return DriftResult{Confidence: timeline.ConfidenceNone, Samples: n}
	}
	var sumX, sumY, sumXY, sumXX float64
	for _, o := range e.obs {
		x := float64(o.WallMS - e.obs[0].WallMS)
		y := float64(o.MediaTime - e.obs[0].MediaTime)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := float64(n)*sumXX - sumX*sumX
	if denom == 0 || math.IsNaN(denom) || math.IsInf(denom, 0) {
		return DriftResult{Confidence: timeline.ConfidenceNone, Samples: n}
	}
	slope := (float64(n)*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / float64(n)

	// Residual spread normalized to the nominal clock: RMS error in
	// milliseconds, which is what downstream alignment cares about.
	var sq float64
	for _, o := range e.obs {
		x := float64(o.WallMS - e.obs[0].WallMS)
		y := float64(o.MediaTime - e.obs[0].MediaTime)
		pred := slope*x + intercept
		sq += (y - pred) * (y - pred)
	}
	rmsMedia := math.Sqrt(sq / float64(n))
	rmsMS := rmsMedia / e.nominalSlope

	latest := e.obs[n-1]
	xLast := float64(latest.WallMS - e.obs[0].WallMS)
	nominalY := e.nominalSlope * xLast
	actualY := slope*xLast + intercept
	correction := int64(math.Round(nominalY - actualY))

	return DriftResult{
		PPM:        (slope - e.nominalSlope) / e.nominalSlope * 1e6,
		Correction: correction,
		Confidence: confidenceFromRMS(rmsMS),
		Samples:    n,
	}
}

// confidenceFromRMS maps residual RMS error (ms) to a confidence label.
// The thresholds reflect the 2 ms alignment target of the pipeline.
func confidenceFromRMS(rmsMS float64) timeline.Confidence {
	switch {
	case rmsMS <= 1.0:
		return timeline.ConfidenceHigh
	case rmsMS <= 5.0:
		return timeline.ConfidenceMedium
	default:
		return timeline.ConfidenceLow
	}
}

// Reset empties the estimator.
func (e *DriftEstimator) Reset() { e.obs = nil }
