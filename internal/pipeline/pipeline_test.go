package pipeline

import (
	"testing"

	"subsync/internal/cli"
	"subsync/internal/demo"
	"subsync/internal/model"
	"subsync/internal/overlap"
	"subsync/internal/timeline"
)

func demoOpts(t *testing.T, target model.Format, strategy overlap.Strategy) *cli.Options {
	t.Helper()
	fps, err := timeline.ParseFPS("24000/1001")
	if err != nil {
		t.Fatal(err)
	}
	return &cli.Options{
		InputFormat:  model.FormatSRT,
		OutputFormat: target,
		FPS:          fps,
		Overlap:      strategy,
		Track:        "default",
		MaxDeltaMS:   42,
	}
}

func TestRunDemoSRTToVTT(t *testing.T) {
	raw, err := demo.EncodeUTF16LE(demo.SRTFixture())
	if err != nil {
		t.Fatal(err)
	}
	opts := demoOpts(t, model.FormatVTT, overlap.Clip)
	out, rep, summary, cues, err := Run(opts, raw, "")
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty rendered output")
	}
	if summary.InputCount != 8 || summary.OutputCount != 8 || summary.LostCount != 0 {
		t.Errorf("counts wrong: %+v", summary)
	}
	if summary.OverlapCount != 1 {
		t.Errorf("overlaps = %d, want 1", summary.OverlapCount)
	}
	if summary.Encoding != "UTF-16LE" {
		t.Errorf("encoding = %q, want UTF-16LE", summary.Encoding)
	}
	if summary.MaxDeltaMS > 42 {
		t.Errorf("delta %dms exceeds demo budget", summary.MaxDeltaMS)
	}
	if len(rep.PerCue) != 8 || len(cues) != 8 {
		t.Errorf("verification size mismatch: perCue=%d cues=%d", len(rep.PerCue), len(cues))
	}
	for _, cr := range rep.PerCue {
		if !cr.Matched {
			t.Errorf("cue %q did not round-trip: %q", cr.Cue.ID, cr.Issue)
		}
	}
}

func TestRunAllTargetFormats(t *testing.T) {
	raw, err := demo.EncodeUTF16LE(demo.SRTFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []model.Format{model.FormatSRT, model.FormatVTT, model.FormatASS} {
		opts := demoOpts(t, target, overlap.Clip)
		_, rep, summary, _, err := Run(opts, raw, "")
		if err != nil {
			t.Fatalf("%s target: %v", target, err)
		}
		if summary.LostCount != 0 || rep.Missing != 0 {
			t.Errorf("%s: lost=%d missing=%d", target, summary.LostCount, rep.Missing)
		}
	}
}

func TestThresholdExceeded(t *testing.T) {
	opts := demoOpts(t, model.FormatVTT, overlap.Clip)
	opts.MaxDeltaMS = 42
	opts.MaxDropped = 0

	okSummary := fakeSummary(10, 0)
	if ThresholdExceeded(fakeReport(0), okSummary, opts) {
		t.Error("within-budget run flagged as exceeded")
	}
	badDelta := fakeSummary(43, 0)
	if !ThresholdExceeded(fakeReport(0), badDelta, opts) {
		t.Error("delta over budget not flagged")
	}
	badDrop := fakeSummary(0, 1)
	if !ThresholdExceeded(fakeReport(0), badDrop, opts) {
		t.Error("drop over budget not flagged")
	}
}
