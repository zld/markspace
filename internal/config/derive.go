// Package config derives an explainable conversion configuration for a track
// from its timeline and the delivery target. Each chosen setting records the
// evidence (the spec diff or constraint that forced it), and constraints that
// cannot be met are marked unsatisfiable rather than silently weakened.
package config

import (
	"fmt"

	"mediasync/internal/model"
	"mediasync/internal/spec"
)

// Step is one conversion action with the evidence that justifies it.
type Step struct {
	Field  string `json:"field"`
	From   int64  `json:"from"`
	To     int64  `json:"to"`
	Reason string `json:"reason"`
}

// Decision is the derived configuration for one track.
type Decision struct {
	TrackID       string         `json:"track_id"`
	TargetBitrate int64          `json:"target_bitrate_bps"`
	Quality       int            `json:"quality"` // 0..100
	Steps         []Step         `json:"steps"`
	EstimatedNS   model.NS       `json:"estimated_processing_ns"`
	Unsatisfiable []model.Failure `json:"unsatisfiable,omitempty"`
	Eligible      bool           `json:"eligible"`
}

// costPerSampleNS is the calibrated per-sample processing cost. It is a fixed
// integer estimate, deterministic across runs — no wall-clock measurement.
const costPerSampleNS model.NS = 400_000 // 0.4 ms per accounted sample

// EstimateCost derives a processing-time estimate from the number of real
// timeline samples plus a fixed per-track overhead.
func EstimateCost(tl *model.TrackTimeline) model.NS {
	samples := 0
	for i := range tl.Entries {
		if tl.Entries[i].Class == model.EntrySample {
			samples++
		}
	}
	const overhead model.NS = 2_000_000 // 2 ms fixed overhead
	return overhead + model.NS(samples)*costPerSampleNS
}

// Derive builds the conversion decision. It never returns nil; an ineligible
// track (failed, rejected, or blocked) gets Eligible=false with reasons.
func Derive(meta model.TrackMeta, tl *model.TrackTimeline,
	rep *spec.Report, target model.TargetSpec, schedulable bool) *Decision {

	d := &Decision{TrackID: meta.TrackID, Quality: defaultQuality(meta)}

	if !schedulable {
		d.Eligible = false
		d.Unsatisfiable = append(d.Unsatisfiable, model.NewTrackFailure(
			meta.TrackID, model.StageConfig, model.ErrDependencyMissing,
			"track dependency not satisfied; no configuration generated"))
		return d
	}
	if rep != nil && rep.RejectReason != "" {
		d.Eligible = false
		d.Unsatisfiable = append(d.Unsatisfiable, model.NewTrackFailure(
			meta.TrackID, model.StageConfig, model.ErrQualityBelowFloor,
			"layout rejected: "+rep.RejectReason))
		return d
	}

	// Conversion steps are driven one-for-one by spec diffs.
	if rep != nil {
		for _, diff := range rep.Diffs {
			d.Steps = append(d.Steps, stepFromDiff(diff))
		}
	}

	d.TargetBitrate = chooseBitrate(meta, target)
	d.Quality = clampQuality(d.Quality, target.MinQuality)
	d.EstimatedNS = EstimateCost(tl)

	// A floor above the hard cap cannot be delivered at any configuration.
	if target.MinQuality > qualityHardCap {
		d.Unsatisfiable = append(d.Unsatisfiable, model.NewTrackFailure(
			meta.TrackID, model.StageConfig, model.ErrQualityBelowFloor, fmt.Sprintf(
				"required quality floor %d exceeds hard cap %d",
				target.MinQuality, qualityHardCap)))
		d.Eligible = false
	}
	// Constraint checks; each failure is a distinct unsatisfiable marker.
	if target.MaxBitrate > 0 && d.TargetBitrate > target.MaxBitrate {
		d.Unsatisfiable = append(d.Unsatisfiable, model.NewTrackFailure(
			meta.TrackID, model.StageConfig, model.ErrBitrateExceeded, fmt.Sprintf(
				"minimum viable bitrate %d bps exceeds target %d bps",
				d.TargetBitrate, target.MaxBitrate)))
		d.Eligible = false
	}
	if target.BudgetNS > 0 && d.EstimatedNS > target.BudgetNS {
		d.Unsatisfiable = append(d.Unsatisfiable, model.NewTrackFailure(
			meta.TrackID, model.StageConfig, model.ErrBudgetExceeded, fmt.Sprintf(
				"estimated processing %dns exceeds budget %dns",
				d.EstimatedNS, target.BudgetNS)))
		d.Eligible = false
	}
	if target.MinQuality > 0 && d.Quality < target.MinQuality {
		d.Unsatisfiable = append(d.Unsatisfiable, model.NewTrackFailure(
			meta.TrackID, model.StageConfig, model.ErrQualityBelowFloor, fmt.Sprintf(
				"quality %d below required floor %d", d.Quality, target.MinQuality)))
		d.Eligible = false
	}

	d.Eligible = d.Eligible || len(d.Unsatisfiable) == 0
	return d
}

// defaultQuality is a deterministic codec-specific starting quality.
func defaultQuality(meta model.TrackMeta) int {
	switch meta.Codec {
	case model.CodecOpus:
		return 82
	case model.CodecAAC:
		return 75
	default:
		return 70
	}
}

// chooseBitrate picks the lowest viable bitrate that satisfies the target
// ceiling where possible; otherwise the codec floor, which then fails the
// ceiling check above so the conflict stays visible.
func chooseBitrate(meta model.TrackMeta, target model.TargetSpec) int64 {
	floor := int64(64_000)
	if meta.Codec == model.CodecAAC {
		floor = 56_000
	}
	if target.MaxBitrate > 0 && target.MaxBitrate >= floor {
		return target.MaxBitrate
	}
	return floor
}

const qualityHardCap = 100

func clampQuality(q, floor int) int {
	if floor > q {
		q = floor
	}
	if q > qualityHardCap {
		return qualityHardCap
	}
	return q
}

func stepFromDiff(diff spec.Diff) Step {
	s := Step{Field: diff.Field, Reason: "target spec requires " + diff.Field}
	s.From = toInt64(diff.Source)
	s.To = toInt64(diff.Target)
	return s
}

func toInt64(v interface{}) int64 {
	switch x := v.(type) {
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case nil:
		return 0
	default:
		return 0
	}
}
