// Package quality aggregates the cross-subsystem measurements into one
// deterministic quality report and checks the acceptance invariants
// (bitrate deviation, cut alignment, recovery after loss).
package quality

import (
	"encoding/json"
	"sort"

	"mediaverify/internal/bitrate"
	"mediaverify/internal/errs"
	"mediaverify/internal/segment"
)

// Violation is one failed acceptance check.
type Violation struct {
	Area    string `json:"area"`
	Message string `json:"message"`
}

// Report is the terminal quality document.
type Report struct {
	TargetBPS         int64       `json:"target_bps"`
	ActualBPS         int64       `json:"actual_bps"`
	BitrateErrorPct   float64     `json:"bitrate_error_pct"`
	Segments          int         `json:"segments"`
	AlignmentMaxFrame int         `json:"alignment_max_frames"`
	SubtitleSpans     int         `json:"subtitle_spans"`
	RecoveredSegments int         `json:"recovered_segments"`
	DegradedIntervals int         `json:"degraded_intervals"`
	ReplayCount       int         `json:"replay_count"`
	MaxDeviationPct   float64     `json:"max_bitrate_error_pct"`
	Violations        []Violation `json:"violations"`
}

// Inputs bundles the data the report is built from.
type Inputs struct {
	ActiveRung  bitrate.Rendition
	Decisions   []bitrate.Decision
	Plan        segment.Plan
	SubtitleN   int
	ReplayCount int
}

// Build computes every metric and evaluates the hard limits:
//   - active rung deviation <= 15%
//   - every rendition on every segment within 15%
//   - cut alignment <= one frame
func Build(in Inputs) (Report, error) {
	r := Report{
		TargetBPS:         in.ActiveRung.TargetBPS,
		ActualBPS:         in.ActiveRung.ActualBPS,
		Segments:          len(in.Plan.Segments),
		AlignmentMaxFrame: in.Plan.AlignmentMaxFrame,
		SubtitleSpans:     in.SubtitleN,
		RecoveredSegments: in.Plan.RecoveredCount(),
		DegradedIntervals: len(in.Plan.DegradedIntervals()),
		ReplayCount:       in.ReplayCount,
	}
	r.BitrateErrorPct = bitrate.ErrorPct(in.ActiveRung)
	maxDev := r.BitrateErrorPct
	for _, d := range in.Decisions {
		for _, rend := range d.Renditions {
			if pct := bitrate.ErrorPct(rend); pct > maxDev {
				maxDev = pct
			}
		}
	}
	r.MaxDeviationPct = maxDev

	if r.BitrateErrorPct > bitrate.MaxDeviationPct {
		r.Violations = append(r.Violations, Violation{
			Area:    "bitrate",
			Message: "active rung deviation exceeds 15%",
		})
	}
	for _, d := range in.Decisions {
		for _, rend := range d.Renditions {
			if pct := bitrate.ErrorPct(rend); pct > bitrate.MaxDeviationPct {
				r.Violations = append(r.Violations, Violation{
					Area:    "bitrate",
					Message: "rung deviation exceeds 15%",
				})
				break
			}
		}
	}
	if r.AlignmentMaxFrame > 1 {
		r.Violations = append(r.Violations, Violation{
			Area:    "segment",
			Message: "cut alignment exceeds one frame",
		})
	}
	// Deterministic violation ordering for stable hashing.
	sort.SliceStable(r.Violations, func(i, j int) bool {
		if r.Violations[i].Area != r.Violations[j].Area {
			return r.Violations[i].Area < r.Violations[j].Area
		}
		return r.Violations[i].Message < r.Violations[j].Message
	})
	return r, nil
}

// AssertAcceptance maps a report with violations to the internal error class,
// which the command adapter turns into exit code 1.
func (r Report) AssertAcceptance() error {
	if len(r.Violations) == 0 {
		return nil
	}
	raw, _ := json.Marshal(r.Violations)
	return errs.New(errs.KindInternal, "quality", "invariant failures: %s", raw)
}
