// Package report renders the pipeline outcome as a stable JSON summary
// for terminal consumption and regression diffing.
package report

import (
	"encoding/json"
	"io"

	"rtx/internal/model"
	"rtx/internal/pipeline"
	"rtx/internal/transform"
)

// Item is one per-finding line: where, which stage, the conclusion and
// the recovery action.
type Item struct {
	Offset     int64  `json:"offset"`
	Stage      string `json:"stage"`
	Reason     string `json:"reason"`
	Conclusion string `json:"conclusion"`
	Recovery   string `json:"recovery"`
}

// RangeInfo locates one anomalous interval.
type RangeInfo struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
}

// Summary is the top-level JSON document printed by the CLI.
type Summary struct {
	PacketTotal    int                    `json:"packet_total"`
	DuplicateCount int                    `json:"duplicate_count"`
	ReorderedCount int                    `json:"reordered_count"`
	LateCount      int                    `json:"late_count"`
	DriftPPM       float64                `json:"drift_ppm"`
	Quality        float64                `json:"quality"`
	Coverage       float64                `json:"coverage"`
	BitrateBPS     int64                  `json:"bitrate_bps"`
	BudgetMS       int64                  `json:"budget_ms"`
	BudgetActualMS float64                `json:"budget_ms_actual"`
	Feasible       bool                   `json:"feasible"`
	SegmentCount   int                    `json:"segment_count"`
	AnomalyRanges  []RangeInfo            `json:"anomaly_ranges"`
	Constraints    []transform.Constraint `json:"constraints"`
	Checks         []pipeline.Check       `json:"checks"`
	Items          []Item                 `json:"items"`
	ExitCode       int                    `json:"exit_code"`
}

// Exit codes: 0 all thresholds pass, 1 structural/parse failure,
// 2 budget infeasible.
const (
	ExitOK         = 0
	ExitParse      = 1
	ExitInfeasible = 2
)

// Build assembles the summary from the three pipeline stages and
// decides the process exit code.
func Build(in *pipeline.Input, a *pipeline.Analysis, v *pipeline.Validation) *Summary {
	s := &Summary{
		PacketTotal:    in.RTP.Stats.Total,
		DuplicateCount: in.RTP.Stats.Duplicates,
		ReorderedCount: in.RTP.Stats.Reordered,
		LateCount:      in.RTP.Stats.Late,
		DriftPPM:       a.Drift.PPM,
		Quality:        a.Quality.Score,
		Coverage:       v.ValidCoverage,
		BitrateBPS:     a.BitrateBPS,
		BudgetMS:       a.Config.BudgetMS,
		BudgetActualMS: a.Config.BudgetActualMS,
		Feasible:       a.Config.Feasible,
		SegmentCount:   len(a.Segments),
		Constraints:    a.Config.Constraints,
		Checks:         v.Checks,
	}
	addAnomaly := func(an model.Anomaly) {
		s.Items = append(s.Items, Item{
			Offset: an.Offset, Stage: an.Stage, Reason: an.Reason,
			Conclusion: an.Message, Recovery: an.Recovery,
		})
	}
	for _, an := range in.WAV.Anomaly {
		addAnomaly(an)
		s.AnomalyRanges = append(s.AnomalyRanges, RangeInfo{
			Stage: an.Stage, Reason: an.Reason, Start: an.Offset, End: an.Offset + max(an.RawValue, 1),
		})
	}
	for _, an := range in.RTP.Anomalies {
		addAnomaly(an)
	}
	for _, an := range in.Unwrapped.Anomalies {
		addAnomaly(an)
	}
	for _, r := range in.Unwrapped.Isolated {
		s.AnomalyRanges = append(s.AnomalyRanges, RangeInfo{
			Stage: model.StageUnwrap, Reason: "isolated", Start: r.Start, End: r.End,
		})
	}
	for _, an := range a.Anomalies {
		addAnomaly(an)
	}
	for _, r := range a.Drift.Exceeds {
		s.AnomalyRanges = append(s.AnomalyRanges, RangeInfo{
			Stage: model.StageClock, Reason: model.ReasonDrift, Start: r.Start, End: r.End,
		})
	}
	for _, an := range v.Anomalies {
		addAnomaly(an)
	}
	s.ExitCode = ExitOK
	if !a.Config.Feasible {
		// Infeasible configuration (budget or any other business
		// threshold): the dedicated infeasible exit code.
		s.ExitCode = ExitInfeasible
	}
	return s
}

// Write renders the summary as indented JSON.
func Write(w io.Writer, s *Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}
