// Package policy turns quality thresholds and budget limits into deterministic decisions.
package policy

import (
	"math"
)

// Target describes delivery targets and hard policy thresholds.
type Target struct {
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        int     `json:"fps"`
	QualityMin float64 `json:"quality_min"`
	BudgetMbps float64 `json:"budget_mbps"`
	AudioKbps  int     `json:"audio_kbps"`
	Tolerance  float64 `json:"tolerance"` // allowed relative bitrate/quality tolerance
}

// Stats are reproducible stream and container statistics used without decoding media.
type Stats struct {
	DurationMS       int64   `json:"duration_ms"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	FPS              int     `json:"fps"`
	SampleCount      int     `json:"sample_count"`
	KeyframeCount    int     `json:"keyframe_count"`
	MaxKeyframeGapMS int64   `json:"max_keyframe_gap_ms"`
	VideoBytes       int64   `json:"video_bytes"`
	AudioBytes       int64   `json:"audio_bytes"`
	ContainerBytes   int64   `json:"container_bytes"`
	AnomalyPenalty   float64 `json:"anomaly_penalty"`
}

// Deviation explains one unmet or relaxed constraint.
type Deviation struct {
	Constraint string  `json:"constraint"`
	Expected   float64 `json:"expected"`
	Actual     float64 `json:"actual"`
	Delta      float64 `json:"delta"`
	Unit       string  `json:"unit"`
}

// Decision is the explainable result of policy evaluation.
type Decision struct {
	Feasible             bool        `json:"feasible"`
	QualityPass          bool        `json:"quality_pass"`
	QualityScore         float64     `json:"quality_score"`
	TargetVideoKbps      int         `json:"target_video_kbps"`
	TargetAudioKbps      int         `json:"target_audio_kbps"`
	TargetTotalKbps      int         `json:"target_total_kbps"`
	MaximumTotalKbps     int         `json:"maximum_total_kbps"`
	RequiredVideoKbps    int         `json:"required_video_kbps"`
	RequiredTotalKbps    int         `json:"required_total_kbps"`
	EstimatedOutputBytes int64       `json:"estimated_output_bytes"`
	BudgetDeviationMbps  float64     `json:"budget_deviation_mbps"`
	Deviations           []Deviation `json:"deviations,omitempty"`
	Reasons              []string    `json:"reasons,omitempty"`
}

// DefaultTarget fills delivery-oriented defaults while preserving explicit zero rejection elsewhere.
func DefaultTarget(budgetMbps, qualityMin float64, chunkSamples int) Target {
	_ = chunkSamples // chunking is a plan concern; accepted here for stable demo construction.
	return Target{
		Width: 1920, Height: 1080, FPS: 30, QualityMin: qualityMin,
		BudgetMbps: budgetMbps, AudioKbps: 192, Tolerance: 0.05,
	}
}

// BitrateKbps returns the average stream bitrate represented by a byte count and duration.
func BitrateKbps(bytes int64, durationMS int64) float64 {
	if durationMS <= 0 {
		return 0
	}
	return float64(bytes) * 8.0 / float64(durationMS) // bytes*8 / milliseconds = kbps
}

// QualityScore computes a conservative, reproducible packaging/bitrate quality proxy in [0,1].
// It explicitly is not a subjective or decoded audiovisual quality estimate.
func QualityScore(s Stats) float64 {
	if s.DurationMS <= 0 || s.Width <= 0 || s.Height <= 0 || s.FPS <= 0 {
		return 0
	}
	videoKbps := BitrateKbps(s.VideoBytes, s.DurationMS)
	pixelsPerSecond := float64(s.Width) * float64(s.Height) * float64(s.FPS)
	bitsPerPixelFrame := videoKbps * 1000 / pixelsPerSecond

	// The knee is deliberately simple and testable: 0.12 bits/pixel/frame reaches ~0.96.
	bitrateScore := 1 - math.Exp(-bitsPerPixelFrame/0.035)

	keyRatio := 0.0
	if s.SampleCount > 0 {
		keyRatio = float64(s.KeyframeCount) / float64(s.SampleCount)
	}
	// A 2-second maximum GOP is ideal; 10 seconds scores poorly.
	gopScore := 1.0
	if s.MaxKeyframeGapMS > 2000 {
		gopScore = math.Max(0.35, 1.0-float64(s.MaxKeyframeGapMS-2000)/12000.0)
	}
	densityScore := math.Min(1.0, keyRatio*24) // one keyframe per 24 samples is full marks
	score := 0.72*bitrateScore + 0.14*gopScore + 0.14*densityScore
	score -= math.Max(0, s.AnomalyPenalty)
	return math.Round(math.Max(0, math.Min(1, score))*10000) / 10000
}

// Evaluate computes the constrained configuration and every relevant deviation.
func Evaluate(inDigest string, in Stats, target Target) Decision {
	if target.Tolerance == 0 {
		target.Tolerance = 0.05
	}
	if target.AudioKbps == 0 {
		target.AudioKbps = 192
	}
	maxTotalKbps := int(math.Round(target.BudgetMbps * 1000))
	maxVideoKbps := maxTotalKbps - target.AudioKbps

	score := QualityScore(in)
	d := Decision{
		QualityScore:     score,
		QualityPass:      score+target.Tolerance >= target.QualityMin,
		MaximumTotalKbps: maxTotalKbps,
		TargetAudioKbps:  target.AudioKbps,
	}

	pixelsPerSecond := float64(target.Width) * float64(target.Height) * float64(target.FPS)
	// Quality floor maps to a conservative bits-per-pixel requirement.
	requiredBPP := 0.055 + target.QualityMin*0.075
	requiredVideo := int(math.Ceil(pixelsPerSecond * requiredBPP / 1000))
	sourceVideo := int(math.Round(BitrateKbps(in.VideoBytes, in.DurationMS)))
	if sourceVideo > 0 && sourceVideo < requiredVideo {
		// Repacking cannot create coded detail; retain source-aware target but record the gap.
		requiredVideo = int(math.Ceil(math.Max(float64(requiredVideo), float64(sourceVideo))))
	}
	d.RequiredVideoKbps = requiredVideo
	d.RequiredTotalKbps = requiredVideo + target.AudioKbps

	d.TargetVideoKbps = minInt(requiredVideo, maxInt(0, maxVideoKbps))
	d.TargetTotalKbps = d.TargetVideoKbps + target.AudioKbps
	d.EstimatedOutputBytes = int64(math.Ceil(float64(d.TargetTotalKbps) * float64(in.DurationMS) / 8.0))

	if d.RequiredTotalKbps > maxTotalKbps {
		d.Feasible = false
		overMbps := float64(d.RequiredTotalKbps-maxTotalKbps) / 1000.0
		d.BudgetDeviationMbps = math.Round(overMbps*1000) / 1000
		d.Deviations = append(d.Deviations, Deviation{
			Constraint: "bitrate_budget", Expected: target.BudgetMbps,
			Actual: float64(d.RequiredTotalKbps) / 1000, Delta: d.BudgetDeviationMbps, Unit: "Mbps",
		})
		d.Reasons = append(d.Reasons, "quality floor requires more bitrate than the budget allows")
	} else {
		d.Feasible = true
		d.BudgetDeviationMbps = math.Round((float64(d.TargetTotalKbps)/1000-target.BudgetMbps)*1000) / 1000
	}

	if !d.QualityPass {
		d.Feasible = false
		d.Deviations = append(d.Deviations, Deviation{
			Constraint: "quality_min", Expected: target.QualityMin, Actual: score,
			Delta: math.Round((target.QualityMin-score)*10000) / 10000, Unit: "score",
		})
		d.Reasons = append(d.Reasons, "reproducible stream statistic is below the quality threshold")
	}
	if in.MaxKeyframeGapMS > 4000 {
		d.Deviations = append(d.Deviations, Deviation{
			Constraint: "keyframe_interval", Expected: 4000, Actual: float64(in.MaxKeyframeGapMS),
			Delta: float64(in.MaxKeyframeGapMS - 4000), Unit: "ms",
		})
		d.Reasons = append(d.Reasons, "sparse keyframes reduce seek robustness")
	}
	if inDigest == "" {
		d.Feasible = false
		d.Reasons = append(d.Reasons, "input digest is empty")
	}
	return d
}

// WithinBudgetTolerance reports whether actual total bitrate is within relative tolerance.
func WithinBudgetTolerance(targetKbps, actualKbps int, tolerance float64) bool {
	if targetKbps <= 0 {
		return false
	}
	return math.Abs(float64(actualKbps-targetKbps))/float64(targetKbps) <= tolerance
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
