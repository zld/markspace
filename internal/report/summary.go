// Package report builds deterministic per-track and job summaries.
package report

import (
	"fmt"
	"strings"

	"medpipe/internal/clock"
)

// Interval is an inclusive/exclusive tick range on a track time base.
type Interval struct {
	StartTick int64 `json:"start_tick"`
	EndTick   int64 `json:"end_tick_exclusive"`
}

// TrackConclusion is the auditable result for one source track.
type TrackConclusion struct {
	ID                 string            `json:"id"`
	SourceFile         string            `json:"source_file"`
	Verdict            string            `json:"verdict"`
	Detail             string            `json:"detail,omitempty"`
	Samples            int               `json:"samples"`
	SyncSamples        []int             `json:"sync_samples,omitempty"`
	CheckpointSample   int               `json:"checkpoint_sample"`
	SampleRange        [2]int            `json:"sample_range"`
	WrapEvents         int               `json:"wrap_events"`
	EmptyEdits         int               `json:"empty_edits"`
	RepairCount        int               `json:"repair_count"`
	EstimateUnits      int               `json:"estimate_units"`
	ActualUnits        int               `json:"actual_units"`
	VariancePct        float64           `json:"variance_pct"`
	MaxResidualTicks   int64             `json:"max_residual_ticks"`
	Anomaly            *Interval         `json:"anomaly_interval,omitempty"`
	OriginalOffsetTick int64             `json:"original_offset_ticks"`
	RepairOffsetTick   int64             `json:"repair_offset_ticks"`
	Isolation          *clock.Isolation  `json:"isolation,omitempty"`
	Repairs            []clock.Repair    `json:"repairs,omitempty"`
	WrapDetails        []clock.WrapEvent `json:"wrap_details,omitempty"`
	Lines              []string          `json:"terminal_lines,omitempty"`
}

// Summary is the machine-readable terminal and summary.json report.
type Summary struct {
	Schema           string            `json:"schema"`
	Status           string            `json:"status"`
	Verdict          string            `json:"verdict"`
	Tracks           int               `json:"tracks"`
	Samples          int               `json:"samples"`
	WrapEvents       int               `json:"wrap_events"`
	EmptyEdits       int               `json:"empty_edits"`
	Repairs          int               `json:"repairs"`
	EstimateUnits    int               `json:"estimate"`
	ActualUnits      int               `json:"actual"`
	BudgetUnits      int               `json:"budget_units"`
	VariancePct      float64           `json:"variance_pct"`
	MaxResidualTicks int64             `json:"max_residual_ticks"`
	ManifestSHA256   string            `json:"manifest_sha256"`
	ArtifactSHA256   string            `json:"artifact_sha256"`
	JobKey           string            `json:"job_key"`
	Emitted          int               `json:"emitted"`
	SkippedStages    []string          `json:"skipped_stages,omitempty"`
	ArtifactHashes   map[string]string `json:"artifact_hashes,omitempty"`
	TrackDetails     []TrackConclusion `json:"track_details"`
	TerminalLines    []string          `json:"terminal_lines"`
	SummaryLine      string            `json:"summary_line"`
}

// Build aggregates per-track conclusions and applies the pass criteria.
func Build(status, manifestHash, artifactHash, jobKey string, budgetUnits int, tracks []TrackConclusion, skipped []string) Summary {
	s := Summary{
		Schema:         "medpipe/summary/v1",
		Status:         status,
		BudgetUnits:    budgetUnits,
		ManifestSHA256: manifestHash,
		ArtifactSHA256: artifactHash,
		JobKey:         jobKey,
		TrackDetails:   tracks,
		SkippedStages:  skipped,
	}
	for i := range tracks {
		t := &tracks[i]
		t.EstimateUnits = t.Samples * 4
		t.ActualUnits = t.EstimateUnits + t.RepairCount*3
		t.VariancePct = percent(t.ActualUnits-t.EstimateUnits, t.EstimateUnits)
		s.Tracks++
		s.Samples += t.Samples
		s.WrapEvents += t.WrapEvents
		s.EmptyEdits += t.EmptyEdits
		s.Repairs += t.RepairCount
		s.EstimateUnits += t.EstimateUnits
		s.ActualUnits += t.ActualUnits
		if t.MaxResidualTicks > s.MaxResidualTicks {
			s.MaxResidualTicks = t.MaxResidualTicks
		}
	}
	s.VariancePct = percent(s.ActualUnits-s.EstimateUnits, s.EstimateUnits)

	isolated := false
	for _, t := range tracks {
		if t.Isolation != nil || t.Verdict == "ISOLATED" {
			isolated = true
		}
	}
	switch {
	case isolated || status == "isolated":
		s.Verdict = "ISOLATED"
		s.Status = "isolated"
	case status == "paused_budget":
		s.Verdict = "BUDGET_PAUSED"
	case s.VariancePct > 5.0 || s.MaxResidualTicks > 1:
		s.Verdict = "FAIL"
		if s.Status == "ok" {
			s.Status = "fail"
		}
	default:
		s.Verdict = "PASS"
		if s.Status == "" || s.Status == "ok" {
			s.Status = "ok"
		}
	}
	s.TerminalLines = renderLines(&s)
	s.SummaryLine = fmt.Sprintf(
		"tracks=%d samples=%d estimate=%d actual=%d variance_pct=%.2f max_residual_ticks=%d wraps=%d empty_edits=%d repairs=%d verdict=%s emitted=%d",
		s.Tracks, s.Samples, s.EstimateUnits, s.ActualUnits, s.VariancePct,
		s.MaxResidualTicks, s.WrapEvents, s.EmptyEdits, s.Repairs, s.Verdict, s.Emitted,
	)
	// Keep the exact required ordering/tokens easily greppable as a second line.
	s.SummaryLine += "\n" + fmt.Sprintf(
		"tracks=%d estimate=%d actual=%d variance_pct=%.2f max_residual_ticks=%d verdict=%s",
		s.Tracks, s.EstimateUnits, s.ActualUnits, s.VariancePct, s.MaxResidualTicks, s.Verdict,
	)
	return s
}

func renderLines(s *Summary) []string {
	lines := make([]string, 0, len(s.TrackDetails)+4)
	for _, t := range s.TrackDetails {
		start, end := int64(0), int64(0)
		if t.Anomaly != nil {
			start, end = t.Anomaly.StartTick, t.Anomaly.EndTick
		} else if t.Isolation != nil {
			start, end = t.Isolation.IntervalStartTick, t.Isolation.IntervalEndTick
		}
		budgetDecision := "within-5pct"
		if t.VariancePct > 5.0 {
			budgetDecision = "over-5pct"
		}
		lines = append(lines, fmt.Sprintf(
			"track=%s source=%s verdict=%s samples=%d sample_range=%d..%d checkpoint_sync=%d wraps=%d empty_edits=%d",
			t.ID, t.SourceFile, t.Verdict, t.Samples, t.SampleRange[0], t.SampleRange[1],
			t.CheckpointSample, t.WrapEvents, t.EmptyEdits,
		))
		lines = append(lines, fmt.Sprintf(
			"  anomaly_start_tick=%d anomaly_end_tick=%d original_offset_ticks=%d repair_offset_ticks=%d max_residual_ticks=%d",
			start, end, t.OriginalOffsetTick, t.RepairOffsetTick, t.MaxResidualTicks,
		))
		lines = append(lines, fmt.Sprintf(
			"  budget estimate=%d actual=%d variance_pct=%.2f decision=%s",
			t.EstimateUnits, t.ActualUnits, t.VariancePct, budgetDecision,
		))
		for _, extra := range t.Lines {
			lines = append(lines, "  "+extra)
		}
	}
	if len(s.SkippedStages) > 0 {
		lines = append(lines, "skipped_stages="+strings.Join(s.SkippedStages, ","))
	}
	lines = append(lines, fmt.Sprintf(
		"budget total estimate=%d actual=%d limit=%d variance_pct=%.2f verdict=%s",
		s.EstimateUnits, s.ActualUnits, s.BudgetUnits, s.VariancePct, s.Verdict,
	))
	return lines
}

func percent(delta, base int) float64 {
	if base == 0 {
		return 0
	}
	hundredths := (int64(delta)*10000 + int64(base)/2) / int64(base)
	if delta < 0 {
		hundredths = -((-int64(delta)*10000 + int64(base)/2) / int64(base))
	}
	return float64(hundredths) / 100
}
