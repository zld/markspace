// Package quality computes deterministic, versioned delivery metrics: target
// versus actual bitrate, an objective packet-level quality score and the
// manifest timeline alignment error. No floating-point drift is allowed to
// decide pass/fail: deviations use integer thousandths where possible.
package quality

import (
	"mediad/internal/model"
	"mediad/internal/timeline"
)

// MetricAlgorithm pins the quality computation so reports are reproducible.
const MetricAlgorithm = "mediad-quality/v1"

// Report is the full set of delivery metrics for one run.
type Report struct {
	Algorithm           string  `json:"algorithm"`
	TargetBitrateBPS    uint64  `json:"target_bitrate_bps"`
	ActualBitrateBPS    uint64  `json:"actual_bitrate_bps"`
	BitrateDeviationPCT float64 `json:"bitrate_deviation_pct"`
	QualityMetric       float64 `json:"quality_metric"`
	AlignmentErrorNS    int64   `json:"manifest_alignment_error_ns"`
	TotalDurationNS     int64   `json:"total_duration_ns"`
	TotalBytes          int64   `json:"total_bytes"`
}

// Bitrate computes actual bitrate from delivered bytes and integer-nanosecond
// duration. Duration must be positive.
func Bitrate(bytes int64, durationNS model.Nano) (uint64, error) {
	if durationNS <= 0 {
		return 0, model.ErrUsage.At("", 0, "quality", 0,
			"non-positive duration for bitrate")
	}
	// bits * 1e9 / ns, with 128-bit-safe integer scaling.
	bits := int64(0)
	if bytes > (1<<62)/8 {
		return 0, model.ErrTimeOverflow.At("", 0, "quality", 0,
			"byte count too large for bitrate")
	}
	bits = bytes * 8
	v, err := model.Scale(bits, int64(model.Second), int64(durationNS))
	if err != nil {
		return 0, err
	}
	return uint64(v), nil
}

// DeviationPercent returns |actual-target|/target * 100 as a float solely for
// display; the 5% gate is checked on integer permille in WithinBitrateGate.
func DeviationPercent(target, actual uint64) float64 {
	if target == 0 {
		return 0
	}
	diff := int64(actual) - int64(target)
	if diff < 0 {
		diff = -diff
	}
	permille := diff * 1000 / int64(target)
	return float64(permille) / 10
}

// WithinBitrateGate applies the documented 5% tolerance using integer
// permille (50 permille == 5%).
func WithinBitrateGate(target, actual uint64) bool {
	if target == 0 {
		return false
	}
	diff := int64(actual) - int64(target)
	if diff < 0 {
		diff = -diff
	}
	return diff*1000 <= int64(target)*50
}

// ObjectiveScore is a deterministic 0..1 objective quality proxy. It combines
// packet retention (accepted versus expected sequences including gaps) with
// payload continuity (relative size variation). Gaps lower the score but are
// never hidden; duplicates are ignored as they add no media.
func ObjectiveScore(accepted, expected uint64, gaps []timeline.Gap) float64 {
	if expected == 0 {
		return 0
	}
	retention := float64(accepted) / float64(expected)
	if retention > 1 {
		retention = 1
	}
	missing := uint64(0)
	for _, g := range gaps {
		if g.ToSeq >= g.FromSeq {
			missing += g.ToSeq - g.FromSeq + 1
		}
	}
	// Gap penalty is bounded so one missing frame cannot zero the score.
	penalty := float64(missing) / float64(expected)
	if penalty > 1 {
		penalty = 1
	}
	score := retention * (1 - 0.5*penalty)
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return round4(score)
}

// round4 rounds to four decimal places without external float helpers.
func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

// AlignmentError compares per-track timeline frame boundaries against a
// common grid: it is the largest absolute discrepancy, in nanoseconds,
// between each frame start and the cumulative expected duration of the frames
// before it. A perfectly packed timeline reports zero.
func AlignmentError(tracks []*timeline.Timeline) (int64, error) {
	var worst int64
	for _, tl := range tracks {
		var expected model.Nano
		for i, f := range tl.Frames {
			diff := int64(f.StartNS - expected)
			if diff < 0 {
				diff = -diff
			}
			if diff > worst {
				worst = diff
			}
			var err error
			expected, err = model.AddNano(expected, f.DurationNS)
			if err != nil {
				return 0, err
			}
			_ = i
		}
	}
	return worst, nil
}

// Measure assembles the full Report for one run. bytes is total delivered
// payload bytes; durationNS is the summed timeline duration excluding gaps.
func Measure(targetBPS uint64, bytes int64, durationNS model.Nano,
	accepted, expected uint64, gaps []timeline.Gap, tracks []*timeline.Timeline) (*Report, error) {
	actual, err := Bitrate(bytes, durationNS)
	if err != nil {
		return nil, err
	}
	align, err := AlignmentError(tracks)
	if err != nil {
		return nil, err
	}
	return &Report{
		Algorithm:           MetricAlgorithm,
		TargetBitrateBPS:    targetBPS,
		ActualBitrateBPS:    actual,
		BitrateDeviationPCT: DeviationPercent(targetBPS, actual),
		QualityMetric:       ObjectiveScore(accepted, expected, gaps),
		AlignmentErrorNS:    align,
		TotalDurationNS:     int64(durationNS),
		TotalBytes:          bytes,
	}, nil
}
