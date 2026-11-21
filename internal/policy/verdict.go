// Package policy centralizes the pass/fail decision so that single-file and
// batch analysis can never disagree about what "passing" means.
package policy

import (
	"fmt"
	"math"

	"loudcheck/internal/loudness"
)

// Thresholds are the delivery criteria applied to every measured file.
type Thresholds struct {
	// TargetLUFS is the desired integrated loudness.
	TargetLUFS float64
	// ToleranceLU is the allowed absolute deviation from TargetLUFS.
	ToleranceLU float64
	// MaxTruePeakDBTP is the highest acceptable true peak.
	MaxTruePeakDBTP float64
}

// DefaultThresholds returns the common streaming-music delivery target.
func DefaultThresholds() Thresholds {
	return Thresholds{TargetLUFS: -23, ToleranceLU: 1, MaxTruePeakDBTP: -1}
}

// Validate rejects threshold combinations that cannot produce a meaningful
// verdict.
func (t Thresholds) Validate() error {
	if math.IsNaN(t.TargetLUFS) || math.IsInf(t.TargetLUFS, 0) {
		return fmt.Errorf("policy: target loudness must be finite")
	}
	if t.TargetLUFS > 0 || t.TargetLUFS < -80 {
		return fmt.Errorf("policy: target loudness %v LUFS is outside [-80, 0]", t.TargetLUFS)
	}
	if math.IsNaN(t.ToleranceLU) || t.ToleranceLU < 0 || t.ToleranceLU > 30 {
		return fmt.Errorf("policy: tolerance %v LU is outside [0, 30]", t.ToleranceLU)
	}
	if math.IsNaN(t.MaxTruePeakDBTP) || t.MaxTruePeakDBTP < -60 || t.MaxTruePeakDBTP > 6 {
		return fmt.Errorf("policy: true-peak limit %v dBTP is outside [-60, 6]", t.MaxTruePeakDBTP)
	}
	return nil
}

// Verdict is the outcome of applying Thresholds to one measurement.
type Verdict struct {
	// Measured is false when the file contained no gated audio, for example
	// digital silence or a clip shorter than one 400 ms block. Threshold
	// checks are then reported as false, never as a vacuous pass.
	Measured       bool `json:"measured"`
	IntegratedPass bool `json:"integrated_pass"`
	TruePeakPass   bool `json:"true_peak_pass"`
	Pass           bool `json:"pass"`
	// Failures lists human-readable reasons in a stable order.
	Failures []string `json:"failures,omitempty"`
}

// Evaluate applies thresholds to one measurement. The two checks are kept
// separate so a report can tell an artist which criterion failed.
func Evaluate(m loudness.Result, t Thresholds) Verdict {
	v := Verdict{Measured: m.IntegratedValid}

	switch {
	case !m.IntegratedValid:
		v.Failures = append(v.Failures, "integrated loudness is not measurable (no gated 400 ms blocks)")
	default:
		dev := math.Abs(m.IntegratedLUFS - t.TargetLUFS)
		v.IntegratedPass = dev <= t.ToleranceLU+1e-9
		if !v.IntegratedPass {
			v.Failures = append(v.Failures, fmt.Sprintf(
				"integrated loudness %.2f LUFS is %.2f LU from target %.2f LUFS (tolerance %.2f LU)",
				m.IntegratedLUFS, m.IntegratedLUFS-t.TargetLUFS, t.TargetLUFS, t.ToleranceLU))
		}
	}

	tpDBTP := loudness.DB(m.TruePeak)
	v.TruePeakPass = tpDBTP <= t.MaxTruePeakDBTP+1e-9
	if !v.TruePeakPass {
		v.Failures = append(v.Failures, fmt.Sprintf(
			"true peak %.2f dBTP exceeds limit %.2f dBTP", tpDBTP, t.MaxTruePeakDBTP))
	}
	if math.IsInf(tpDBTP, -1) {
		// A silent file has no peak to violate the limit, but it also has no
		// loudness; the failure above already carries the verdict.
		v.Failures = append(v.Failures, "true peak is undefined (file is digitally silent)")
		v.TruePeakPass = false
	}

	v.Pass = v.Measured && v.IntegratedPass && v.TruePeakPass
	return v
}
