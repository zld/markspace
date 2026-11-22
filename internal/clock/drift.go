package clock

import "math"

// DefaultDriftJumpPPM bounds how far an observed interval rate may move from
// the running estimate before the change is declared a drift mutation. A
// healthy crystal drifts tens of ppm; an abrupt multi-percent jump means the
// time base jumped and must be surfaced, not smoothed away.
const DefaultDriftJumpPPM = 1000.0

// DriftModel applies a static ppm rate correction and watches measured
// intervals for abrupt drift mutations. All arithmetic is float64 over
// integer nanosecond inputs; given identical input samples the output is
// bit-identical on reruns.
type DriftModel struct {
	basePPM   float64
	jumpPPM   float64
	factor    float64 // 1/(1+basePPM/1e6), applied to raw nanoseconds
	estimate  float64 // EWMA of observed residual ppm
	hasSample bool
	mutations int
}

// NewDriftModel builds a model with the default mutation threshold.
func NewDriftModel(basePPM float64) *DriftModel {
	return NewDriftModelWithThreshold(basePPM, DefaultDriftJumpPPM)
}

// NewDriftModelWithThreshold allows tests to tighten the mutation threshold.
func NewDriftModelWithThreshold(basePPM, jumpPPM float64) *DriftModel {
	return &DriftModel{
		basePPM: basePPM,
		jumpPPM: jumpPPM,
		factor:  1.0 / (1.0 + basePPM/1_000_000.0),
	}
}

// Correct maps a device nanosecond timestamp onto the true-rate base. A fast
// device (positive ppm) overcounts time, so values are scaled down.
func (d *DriftModel) Correct(ns int64) int64 {
	return int64(math.Round(float64(ns) * d.factor))
}

// Observe feeds one measured interval alongside its expected (ideal) length.
// An abrupt change of the residual rate beyond jumpPPM returns KindDrift;
// ordinary residuals are folded into an EWMA estimate.
func (d *DriftModel) Observe(track uint32, seq uint64, measured, expected int64) error {
	if expected <= 0 {
		return fail(KindScale, track, seq, "non-positive expected interval %d", expected)
	}
	residualPPM := (float64(measured-expected) / float64(expected)) * 1_000_000.0
	if d.hasSample && math.Abs(residualPPM-d.estimate) > d.jumpPPM {
		d.mutations++
		return fail(KindDrift, track, seq,
			"drift jumped from %.1f ppm to %.1f ppm", d.estimate, residualPPM)
	}
	const ewmaAlpha = 0.25
	if !d.hasSample {
		d.estimate = residualPPM
		d.hasSample = true
	} else {
		d.estimate = ewmaAlpha*residualPPM + (1-ewmaAlpha)*d.estimate
	}
	return nil
}

// EstimatedPPM returns the EWMA residual rate in ppm.
func (d *DriftModel) EstimatedPPM() float64 { return d.estimate }

// BasePPM returns the configured static correction.
func (d *DriftModel) BasePPM() float64 { return d.basePPM }

// Mutations counts drift jumps observed since creation.
func (d *DriftModel) Mutations() int { return d.mutations }
