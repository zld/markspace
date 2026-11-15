// Package plan turns per-input complexity samples into per-segment target
// bitrates that respect a total average-bitrate budget and the encoder
// minimum bitrate.
package plan

import (
	"errors"
	"fmt"
	"math"

	"gonum.org/v1/gonum/stat"
)

// ErrInvalidInput marks malformed planning inputs (negative durations,
// empty or non-finite complexity samples, ...).
var ErrInvalidInput = errors.New("invalid planning input")

// SegmentSpec is the planner output for one segment.
type SegmentSpec struct {
	Index            int
	StartSec         float64
	DurationSec      float64
	Complexity       float64
	TargetKbps       float64
	Feasible         bool
	InfeasibleReason string
}

// Validate checks the scalar planning parameters and complexity samples.
func Validate(duration, segmentSec, targetKbps, minKbps float64, samples []float64) error {
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return fmt.Errorf("%w: duration must be a positive finite number, got %v", ErrInvalidInput, duration)
	}
	if math.IsNaN(segmentSec) || math.IsInf(segmentSec, 0) || segmentSec <= 0 {
		return fmt.Errorf("%w: segment-seconds must be a positive finite number, got %v", ErrInvalidInput, segmentSec)
	}
	if segmentSec > duration {
		return fmt.Errorf("%w: segment-seconds %g exceeds duration %g", ErrInvalidInput, segmentSec, duration)
	}
	if math.IsNaN(targetKbps) || math.IsInf(targetKbps, 0) || targetKbps <= 0 {
		return fmt.Errorf("%w: target-kbps must be a positive finite number, got %v", ErrInvalidInput, targetKbps)
	}
	if math.IsNaN(minKbps) || math.IsInf(minKbps, 0) || minKbps <= 0 {
		return fmt.Errorf("%w: min-kbps must be a positive finite number, got %v", ErrInvalidInput, minKbps)
	}
	if len(samples) == 0 {
		return fmt.Errorf("%w: complexity sample set is empty", ErrInvalidInput)
	}
	for i, v := range samples {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%w: complexity sample %d is not finite: %v", ErrInvalidInput, i, v)
		}
		if v < 0 {
			return fmt.Errorf("%w: complexity sample %d is negative: %v", ErrInvalidInput, i, v)
		}
	}
	return nil
}

// smooth returns a centered moving average of the samples. The window size
// is the average number of samples per segment, so each smoothed value
// summarizes one window of material rather than a single noisy sample.
// Edge windows are clamped to available neighbors; output is deterministic.
func smooth(samples []float64, nSegments int) []float64 {
	n := len(samples)
	radius := n / nSegments
	if radius < 1 {
		radius = 1
	}
	out := make([]float64, n)
	weights := make([]float64, n) // uniform weights, kept for gonum stat.Mean
	for i := range weights {
		weights[i] = 1
	}
	for i := 0; i < n; i++ {
		lo := i - radius
		if lo < 0 {
			lo = 0
		}
		hi := i + radius
		if hi > n-1 {
			hi = n - 1
		}
		out[i] = stat.Mean(samples[lo:hi+1], weights[lo:hi+1])
	}
	return out
}

// Plan computes one target bitrate per segment. The total bit budget
// (target_kbps * duration) is distributed in proportion to each segment's
// smoothed complexity multiplied by its duration, so the realized average
// matches the requested target within 1%.
func Plan(duration, segmentSec, targetKbps, minKbps float64, samples []float64) ([]SegmentSpec, error) {
	if err := Validate(duration, segmentSec, targetKbps, minKbps, samples); err != nil {
		return nil, err
	}

	// Subtract epsilon before ceil so an exact multiple (120s / 10s)
	// does not gain a spurious extra segment from floating-point error.
	nSegments := int(math.Ceil(duration/segmentSec - 1e-9))
	smoothed := smooth(samples, nSegments)

	specs := make([]SegmentSpec, nSegments)
	// Sample j is centered at t = (j + 0.5) * duration / len(samples).
	sampleAt := func(j int) float64 {
		return (float64(j) + 0.5) * duration / float64(len(samples))
	}

	weightSum := 0.0
	for i := 0; i < nSegments; i++ {
		start := float64(i) * segmentSec
		end := math.Min(start+segmentSec, duration)
		if i == nSegments-1 {
			end = duration
		}
		var window []float64
		for j, v := range smoothed {
			t := sampleAt(j)
			if t >= start && t < end || (i == nSegments-1 && t >= start && t <= end) {
				window = append(window, v)
			}
		}
		if len(window) == 0 { // Degenerate dense-segment case: fall back to nearest sample.
			idx := int(float64(len(smoothed)) * (start + 0.5*segmentSec) / duration)
			if idx >= len(smoothed) {
				idx = len(smoothed) - 1
			}
			window = []float64{smoothed[idx]}
		}
		w := make([]float64, len(window))
		for k := range w {
			w[k] = 1
		}
		complexity := stat.Mean(window, w)
		if complexity <= 0 {
			complexity = 1e-6 // never give a static segment a zero weight
		}
		specs[i] = SegmentSpec{
			Index:       i,
			StartSec:    start,
			DurationSec: end - start,
			Complexity:  complexity,
			Feasible:    true,
		}
		weightSum += complexity * specs[i].DurationSec
	}

	// Budget allocation: bits_i ∝ complexity_i * duration_i.
	budgetTotal := targetKbps * duration
	for i := range specs {
		share := (specs[i].Complexity * specs[i].DurationSec) / weightSum
		alloc := budgetTotal * share // kbps*seconds assigned to this segment
		specs[i].TargetKbps = alloc / specs[i].DurationSec
	}

	// Absorb floating-point residual into the longest segment, guaranteeing
	// the realized average stays within the 1% budget tolerance.
	realized := 0.0
	for _, s := range specs {
		realized += s.TargetKbps * s.DurationSec
	}
	residual := budgetTotal - realized
	longest := 0
	for i := range specs {
		if specs[i].DurationSec > specs[longest].DurationSec {
			longest = i
		}
	}
	specs[longest].TargetKbps += residual / specs[longest].DurationSec

	for i := range specs {
		if specs[i].TargetKbps < minKbps {
			specs[i].Feasible = false
			specs[i].InfeasibleReason = fmt.Sprintf(
				"segment %d target %.2f kbps below encoder minimum %.2f kbps",
				i+1, specs[i].TargetKbps, minKbps)
		}
	}
	return specs, nil
}

// BudgetError returns the absolute percentage gap between the planned
// average bitrate and the requested target.
func BudgetError(targetKbps, duration float64, specs []SegmentSpec) float64 {
	realized := 0.0
	for _, s := range specs {
		realized += s.TargetKbps * s.DurationSec
	}
	plannedAvg := realized / duration
	return math.Abs(plannedAvg-targetKbps) / targetKbps * 100
}
