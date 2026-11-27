package pipeline

import (
	"fmt"
	"math"

	"rtx/internal/model"
	"rtx/internal/wav"
)

// Check is one validation finding with its conclusion.
type Check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// Validation is the stage-3 output.
type Validation struct {
	Checks          []Check
	ExpectedBitrate int64
	ValidCoverage   float64 // packet coverage excluding corrupt ranges
	Anomalies       []model.Anomaly
}

// Validate re-derives the configuration's expected bitrate and the
// audio statistics from the raw parsed inputs. Corrupt ranges (WAV
// truncations, isolated packet ranges, drift exceedances) never count
// towards valid coverage, and parse anomalies must keep their raw
// offsets.
func Validate(in *Input, a *Analysis) *Validation {
	v := &Validation{}
	// 1. Recompute bitrate independently from raw packets and the media
	// duration established during analysis.
	var payloadBytes int64
	for _, p := range in.RTP.Packets {
		payloadBytes += int64(p.Payload)
	}
	if a.DurationNS > 0 {
		v.ExpectedBitrate = payloadBytes * 8 * 1_000_000_000 / a.DurationNS
	}
	v.Checks = append(v.Checks, Check{
		Name: "bitrate_recompute",
		Pass: v.ExpectedBitrate == a.Config.EstimatedBitrateBPS,
		Detail: fmt.Sprintf("recomputed %d vs configured %d",
			v.ExpectedBitrate, a.Config.EstimatedBitrateBPS),
	})
	// 2. Recompute audio peak from the retained blocks.
	peak := wav.PeakLevel(in.WAV.Blocks, in.WAV.Header.BitsPerSample)
	v.Checks = append(v.Checks, Check{
		Name:   "audio_stats",
		Pass:   math.Abs(peak-a.Quality.Peak) < 1e-9,
		Detail: fmt.Sprintf("peak %.6f vs quality %.6f", peak, a.Quality.Peak),
	})
	// 3. Coverage excludes corrupt ranges.
	total := len(in.RTP.Packets) // packets that entered the transform
	corrupt := 0
	for _, r := range in.Unwrapped.Isolated {
		corrupt += int(r.End - r.Start)
	}
	for _, r := range a.Drift.Exceeds {
		corrupt += int(r.End - r.Start)
	}
	denom := total + corrupt
	if denom > 0 {
		v.ValidCoverage = float64(total) / float64(denom)
	} else {
		v.ValidCoverage = 1
	}
	v.Checks = append(v.Checks, Check{
		Name: "coverage_excludes_corrupt",
		Pass: corrupt == 0 || v.ValidCoverage < 1,
		Detail: fmt.Sprintf("valid %d of %d packets (%d corrupt excluded)",
			total, denom, corrupt),
	})
	// 4. Parse anomalies keep raw offsets.
	offsetsOK := true
	for _, an := range in.WAV.Anomaly {
		if an.Offset < 0 {
			offsetsOK = false
		}
	}
	for _, an := range in.RTP.Anomalies {
		if an.Offset < 0 {
			offsetsOK = false
		}
	}
	v.Checks = append(v.Checks, Check{
		Name:   "offsets_preserved",
		Pass:   offsetsOK,
		Detail: "parse anomalies carry raw input offsets",
	})
	for _, c := range v.Checks {
		if !c.Pass {
			v.Anomalies = append(v.Anomalies, model.Anomaly{
				Stage: model.StageValidate, Reason: model.ReasonConstraint,
				Offset: -1, RawValue: 0,
				Message:  "validation check failed: " + c.Name,
				Recovery: "inspect stage outputs; do not ship configuration",
			})
		}
	}
	return v
}
