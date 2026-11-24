// Package report aggregates delivery metrics: bitrate deviation against a
// target, valid sample ratio, dropped fragments and the maximum alignment
// error between the manifest and the delivered media.
package report

import (
	"fmt"
	"math"
	"sort"

	"mediaroute/internal/model"
)

// Stage identifies errors emitted by this subsystem.
const Stage = "report/metrics"

// Inputs are the raw counts metrics are derived from.
type Inputs struct {
	TargetBPS    int64
	OutputBytes  uint64
	DurationUs   model.Micros
	TotalSamples int
	ValidSamples int
	Dropped      int
	AlignmentUs  model.Micros
	IndexEntries int
	RepairedTfdt int
	MFHDGaps     int
	Truncated    int
	Recovered    int
	Duplicates   int
}

// Metrics is the machine-readable metrics block emitted in the manifest.
type Metrics struct {
	TargetBPS          int64        `json:"target_bps"`
	ActualBPS          int64        `json:"actual_bps"`
	BitrateDeltaPct    float64      `json:"bitrate_delta_pct"`
	ValidSamplePct     float64      `json:"valid_sample_pct"`
	DroppedFragments   int          `json:"dropped_fragments"`
	ManifestAlignUS    model.Micros `json:"manifest_alignment_us"`
	IndexEntries       int          `json:"index_entries"`
	RepairedTfdt       int          `json:"repaired_tfdt"`
	MFHDGaps           int          `json:"mfhd_gaps"`
	TruncatedBoxes     int          `json:"truncated_boxes"`
	RecoveredFragments int          `json:"recovered_fragments"`
	DuplicateSequences int          `json:"duplicate_sequences"`
}

// ActualBPS derives the actual bitrate from known output bytes and duration
// using integer arithmetic where possible. A zero duration is rejected so a
// divide-by-zero cannot masquerade as a perfect match.
func ActualBPS(outputBytes uint64, durationUs model.Micros) (int64, error) {
	if durationUs <= 0 {
		return 0, model.NewTaskError(model.CategoryInvalidConfig, Stage,
			fmt.Sprintf("cannot compute bitrate from non-positive duration %d us", durationUs))
	}
	// bits = bytes*8; bps = bits / seconds = bytes*8*1e6/durationUs
	bits := outputBytes * 8
	if outputBytes > math.MaxUint64/8 {
		return 0, model.NewTaskError(model.CategoryLengthOverflow, Stage, "output byte count overflows bits")
	}
	bps := bits * uint64(model.MicrosPerSecond) / uint64(durationUs)
	if bps > math.MaxInt64 {
		return 0, model.NewTaskError(model.CategoryLengthOverflow, Stage, "bitrate exceeds int64")
	}
	return int64(bps), nil
}

// DeltaPct returns (actual-target)/target*100 as a float for display only.
// The value is derived from already-rounded integers, so it is stable across
// runs and never accumulates.
func DeltaPct(target, actual int64) (float64, error) {
	if target <= 0 {
		return 0, model.NewTaskError(model.CategoryInvalidConfig, Stage,
			fmt.Sprintf("target bitrate must be positive, got %d", target))
	}
	return float64(actual-target) / float64(target) * 100, nil
}

// ValidPct returns the percentage of valid samples, rounded to the nearest
// hundredth so repeated runs print identical values.
func ValidPct(valid, total int) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(valid)/float64(total)*10000) / 100
}

// Compute validates inputs and returns the metrics block.
func Compute(in Inputs) (Metrics, error) {
	if in.TargetBPS <= 0 {
		return Metrics{}, model.NewTaskError(model.CategoryInvalidConfig, Stage,
			"target bitrate must be positive")
	}
	actual, err := ActualBPS(in.OutputBytes, in.DurationUs)
	if err != nil {
		return Metrics{}, err
	}
	delta, err := DeltaPct(in.TargetBPS, actual)
	if err != nil {
		return Metrics{}, err
	}
	return Metrics{
		TargetBPS:          in.TargetBPS,
		ActualBPS:          actual,
		BitrateDeltaPct:    math.Round(delta*100) / 100,
		ValidSamplePct:     ValidPct(in.ValidSamples, in.TotalSamples),
		DroppedFragments:   in.Dropped,
		ManifestAlignUS:    in.AlignmentUs,
		IndexEntries:       in.IndexEntries,
		RepairedTfdt:       in.RepairedTfdt,
		MFHDGaps:           in.MFHDGaps,
		TruncatedBoxes:     in.Truncated,
		RecoveredFragments: in.Recovered,
		DuplicateSequences: in.Duplicates,
	}, nil
}

// TrackSpans derives per-track manifest spans from delivered fragments.
func TrackSpans(tracks []model.Track, frags []model.Fragment) []model.TrackManifest {
	acc := make(map[uint32]*model.TrackManifest, len(tracks))
	for _, t := range tracks {
		acc[t.TrackID] = &model.TrackManifest{
			TrackID: t.TrackID, Timescale: t.Timescale, Handler: t.Handler, Codec: t.Codec,
		}
	}
	for i := range frags {
		f := &frags[i]
		if f.Discarded || f.Truncated {
			continue
		}
		for ti := range f.Tracks {
			ft := &f.Tracks[ti]
			tm, ok := acc[ft.TrackID]
			if !ok {
				tm = &model.TrackManifest{TrackID: ft.TrackID, Timescale: ft.Timescale}
				acc[ft.TrackID] = tm
			}
			if tm.Samples == 0 || ft.StartMicros < tm.StartMicros {
				tm.StartMicros = ft.StartMicros
			}
			if ft.EndMicros > tm.EndMicros {
				tm.EndMicros = ft.EndMicros
			}
			tm.Samples += len(ft.Samples)
			for si := range ft.Samples {
				if ft.Samples[si].IsSync {
					tm.SyncSamples++
				}
			}
		}
	}
	out := make([]model.TrackManifest, 0, len(acc))
	for _, tm := range acc {
		tm.DurationMicros = tm.EndMicros - tm.StartMicros
		if tm.DurationMicros < 0 {
			tm.DurationMicros = 0
		}
		out = append(out, *tm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TrackID < out[j].TrackID })
	return out
}

// AlignManifest compares per-track manifest spans with the rebuilt index and
// returns the worst absolute mismatch in microseconds.
func AlignManifest(ix *model.Index, tracks []model.TrackManifest) model.Micros {
	var worst model.Micros
	for _, tm := range tracks {
		first, last, ok := ix.TrackSpan(tm.TrackID)
		if !ok {
			if tm.Samples > 0 {
				return model.Micros(math.MaxInt64)
			}
			continue
		}
		if d := model.AbsMicros(first, tm.StartMicros); d > worst {
			worst = d
		}
		if d := model.AbsMicros(last, tm.EndMicros); d > worst {
			worst = d
		}
	}
	return worst
}
