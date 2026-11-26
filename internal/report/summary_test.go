package report

import (
	"strings"
	"testing"

	"medpipe/internal/clock"
)

func TestNormalSummaryMatchesBudgetAndVerdict(t *testing.T) {
	tracks := []TrackConclusion{
		{ID: "a", Samples: 6},
		{ID: "b", Samples: 6, EmptyEdits: 1},
		{ID: "c", Samples: 6, RepairCount: 1},
	}
	s := Build("ok", "m", "a", "k", 80, tracks, nil)
	if s.Tracks != 3 || s.Samples != 18 {
		t.Fatalf("tracks/samples=%d/%d", s.Tracks, s.Samples)
	}
	if s.EstimateUnits != 72 || s.ActualUnits != 75 {
		t.Fatalf("units=%d/%d", s.EstimateUnits, s.ActualUnits)
	}
	if s.VariancePct != 4.17 {
		t.Fatalf("variance=%.2f", s.VariancePct)
	}
	if s.Verdict != "PASS" || s.MaxResidualTicks != 0 {
		t.Fatalf("verdict=%s residual=%d", s.Verdict, s.MaxResidualTicks)
	}
	for _, token := range []string{"tracks=3", "estimate=72", "actual=75", "variance_pct=4.17", "max_residual_ticks=0", "verdict=PASS"} {
		if !strings.Contains(s.SummaryLine, token) {
			t.Fatalf("summary line missing %q:\n%s", token, s.SummaryLine)
		}
	}
	if len(s.TerminalLines) < 9 {
		t.Fatalf("expected per-track terminal lines, got %v", s.TerminalLines)
	}
}

func TestIsolatedVerdict(t *testing.T) {
	tracks := []TrackConclusion{{
		ID: "bad", Samples: 3, Verdict: "ISOLATED",
		Isolation: &clock.Isolation{Track: "bad", IntervalStartTick: 10, IntervalEndTick: 20},
	}}
	s := Build("isolated", "m", "a", "k", 80, tracks, []string{"align"})
	if s.Verdict != "ISOLATED" {
		t.Fatalf("verdict=%s", s.Verdict)
	}
	if len(s.SkippedStages) != 1 || s.SkippedStages[0] != "align" {
		t.Fatalf("skipped=%v", s.SkippedStages)
	}
}
