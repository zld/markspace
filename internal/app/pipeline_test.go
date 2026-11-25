package app

import (
	"context"
	"testing"

	"tracecheck/internal/fixture"
	"tracecheck/internal/merge"
	"tracecheck/internal/model"
)

func baseConfig(profile string) Config {
	return Config{
		Profile:        profile,
		Seed:           7,
		Workers:        2,
		Queue:          4,
		LossBudget:     2,
		KeyframeBudget: model.TicksFromMillis(3),
		WindowTicks:    model.TicksFromMillis(100),
	}
}

func TestAnalyzeCleanPasses(t *testing.T) {
	out, err := Analyze(context.Background(), baseConfig(fixture.ProfileClean))
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("clean exit = %d, want 0; summary=%+v", out.ExitCode, out.Report.Summary)
	}
	s := out.Report.Summary
	if s.PacketCount == 0 || s.TrackCount != 1 || s.KeyframeCount == 0 {
		t.Fatalf("clean summary wrong: %+v", s)
	}
	if s.LossCount != 0 || s.DuplicateCount != 0 || s.TruncatedCount != 0 {
		t.Fatalf("clean trace must show no damage: %+v", s)
	}
	if !merge.Monotonic(out.Timeline) {
		t.Fatal("clean timeline must be monotonic")
	}
}

func TestAnalyzeDamagedFailsBudget(t *testing.T) {
	cfg := baseConfig(fixture.ProfileDamaged)
	out, err := Analyze(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 2 {
		t.Fatalf("damaged exit = %d, want 2", out.ExitCode)
	}
	s := out.Report.Summary
	if s.DuplicateCount == 0 {
		t.Fatal("damaged trace must report duplicates > 0")
	}
	if s.LossCount != 3 {
		t.Fatalf("loss count = %d, want 3", s.LossCount)
	}
	if s.TruncatedCount < 1 {
		t.Fatalf("truncated/empty errors = %d, want >= 1", s.TruncatedCount)
	}
	if model.CountCode(out.Report.Findings, model.ErrEmptyPayload) < 1 {
		t.Fatal("empty truncated packet must be reported with a code")
	}
	if s.BadIndexCount < 1 {
		t.Fatalf("bad index regions = %d, want >= 1", s.BadIndexCount)
	}
	if s.LossBudgetPassed || s.KeyframeBudgetPassed || s.Passed {
		t.Fatalf("both budgets must be exceeded: %+v", s)
	}
	// Every packet/index must carry a processing conclusion.
	if len(out.Report.Conclusions) == 0 {
		t.Fatal("damaged report must include per-item conclusions")
	}
	// Damage propagated on both the RTP path and the MP4 path.
	var hasGap, hasBadIndex, hasTimestampRegression bool
	for _, f := range out.Report.Findings {
		if f.Source == model.SourceRTP && f.Code == model.ErrSequenceGap {
			hasGap = true
		}
		if f.Source == model.SourceMP4 && f.Code == model.ErrMissingIndex {
			hasBadIndex = true
		}
		if f.Code == model.ErrTimestampRegress {
			hasTimestampRegression = true
		}
	}
	if !hasGap || !hasBadIndex {
		t.Fatalf("damage must propagate on both paths: gap=%v badIndex=%v", hasGap, hasBadIndex)
	}
	if !hasTimestampRegression {
		t.Fatal("rolled-back RTP timestamp must be detected")
	}
}

func TestAnalyzeCancelReturns130(t *testing.T) {
	cfg := baseConfig(fixture.ProfileCancel)
	cfg.CancelAfter = 5
	cfg.Workers = 2
	cfg.Queue = 2
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel once the pool reports enough work; driven via job signal.
		cancel()
	}()
	out, err := Analyze(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 130 {
		t.Fatalf("cancel exit = %d, want 130", out.ExitCode)
	}
	if !out.Stats.Canceled {
		t.Fatal("stats must report cancellation")
	}
	if out.Stats.MaxQueue > 2 {
		t.Fatalf("queue depth %d exceeded 2", out.Stats.MaxQueue)
	}
	if out.Stats.Completed+out.Stats.NotReceived != out.Stats.Submitted {
		t.Fatalf("job accounting inconsistent: %+v", out.Stats)
	}
}

func TestAnalyzeRejectsUnknownProfile(t *testing.T) {
	cfg := baseConfig("bogus")
	if _, err := Analyze(context.Background(), cfg); err == nil {
		t.Fatal("unknown profile must return an error for exit code 3")
	}
}

func TestAnalyzeDeterministicAcrossRuns(t *testing.T) {
	cfg := baseConfig(fixture.ProfileDamaged)
	first, err := Analyze(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Analyze(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := first.Report.JSON()
	b, _ := second.Report.JSON()
	if string(a) != string(b) {
		t.Fatal("repeated runs must produce identical report JSON")
	}
}
