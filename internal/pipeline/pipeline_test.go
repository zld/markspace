package pipeline

import (
	"path/filepath"
	"testing"

	"mediad/internal/fixture"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/quality"
)

func baseCfg(t *testing.T, scenario string) Config {
	return Config{
		OutDir:        t.TempDir(),
		Scenario:      scenario,
		TargetBitrate: 80_000,
		MinQuality:    0.5,
		CPUBudgetNS:   1 << 40,
		ByteBudget:    1 << 40,
		MaxDeliveryB:  1 << 40,
	}
}

func TestHealthyEndToEnd(t *testing.T) {
	cfg := baseCfg(t, fixture.ScenHealthy)
	p, err := New(cfg, "run-healthy")
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	r := res.Report
	if r.Algorithm != quality.MetricAlgorithm {
		t.Fatalf("algorithm = %q", r.Algorithm)
	}
	if !quality.WithinBitrateGate(r.TargetBitrateBPS, r.ActualBitrateBPS) {
		t.Fatalf("bitrate gate failed: target=%d actual=%d dev=%.3f%%",
			r.TargetBitrateBPS, r.ActualBitrateBPS, r.BitrateDeviationPCT)
	}
	if r.QualityMetric < cfg.MinQuality {
		t.Fatalf("quality %.4f below gate %.2f", r.QualityMetric, cfg.MinQuality)
	}
	if r.AlignmentErrorNS > int64(model.Millisecond) {
		t.Fatalf("alignment error %d ns exceeds 1ms", r.AlignmentErrorNS)
	}
	if r.TotalDurationNS != int64(model.Second) {
		t.Fatalf("50*20ms must be 1s, got %d", r.TotalDurationNS)
	}
}

func TestDuplicateCountedOnceNoExtraDuration(t *testing.T) {
	cfg := baseCfg(t, fixture.ScenDuplicate)
	p, _ := New(cfg, "run-dup")
	res, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Manifest.Tracks[0]
	if tr.Duplicates != 1 {
		t.Fatalf("duplicates = %d want 1", tr.Duplicates)
	}
	if tr.DurationNS != int64(model.Second) {
		t.Fatalf("duplicate must not extend duration: %d", tr.DurationNS)
	}
	if tr.FrameCount != fixture.FrameCount {
		t.Fatalf("frames = %d want %d", tr.FrameCount, fixture.FrameCount)
	}
}

func TestMissingGapRecordedNoFabrication(t *testing.T) {
	cfg := baseCfg(t, fixture.ScenMissing)
	p, _ := New(cfg, "run-miss")
	res, err := p.Run()
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Manifest.Tracks[0]
	if len(tr.Gaps) != 1 || tr.Gaps[0].FromSeq != 20 || tr.Gaps[0].ToSeq != 20 {
		t.Fatalf("gaps = %+v", tr.Gaps)
	}
	if tr.FrameCount != fixture.FrameCount-1 {
		t.Fatalf("frames = %d, gap must not be filled", tr.FrameCount)
	}
	// Duration drops by exactly one 20ms frame.
	if tr.DurationNS != int64(model.Second)-int64(20*model.Millisecond) {
		t.Fatalf("duration = %d", tr.DurationNS)
	}
}

func TestDamagedInputsFailWithStableCode(t *testing.T) {
	cases := map[string]model.Code{
		fixture.ScenBadOpus:  model.ErrOpusTOC,
		fixture.ScenTruncAAC: model.ErrAACHeader,
	}
	for scenario, want := range cases {
		t.Run(scenario, func(t *testing.T) {
			cfg := baseCfg(t, scenario)
			p, _ := New(cfg, "run-bad")
			_, err := p.Run()
			me := model.AsError(err)
			if me.Code != want || me.Track == "" || me.Stage == "" {
				t.Fatalf("want located %s, got %v", want, err)
			}
			if model.ExitStatus(err) != 2 {
				t.Fatal("input failure must exit 2")
			}
		})
	}
}

func TestInterruptAndResumeProducesIdenticalManifest(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		OutDir: dir, Scenario: fixture.ScenHealthy, TargetBitrate: 80_000,
		MinQuality: 0.5, CPUBudgetNS: 1 << 40, ByteBudget: 1 << 40, MaxDeliveryB: 1 << 40,
		InterruptAfter: StageNormalize,
	}
	p1, err := New(cfg, "run-resume")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p1.Run()
	if !IsInterrupted(err) {
		t.Fatalf("want interruption, got %v", err)
	}
	// No manifest yet: export never ran.
	if _, err := manifest.Load(dir); err == nil {
		t.Fatal("manifest must not exist before export")
	}
	// Snapshot the completed normalize artifact hash.
	normBefore, err := readSHA(filepath.Join(dir, timelineRel))
	if err != nil {
		t.Fatal(err)
	}
	// Restart in a fresh process-equivalent runner, without interruption.
	cfg.InterruptAfter = ""
	p2, err := New(cfg, "run-resume")
	if err != nil {
		t.Fatal(err)
	}
	res, err := p2.Run()
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	normAfter, err := readSHA(filepath.Join(dir, timelineRel))
	if err != nil {
		t.Fatal(err)
	}
	if normBefore != normAfter {
		t.Fatal("reused stage artifact changed on resume")
	}
	// A second verify run must leave the manifest byte-identical.
	manifestBefore, err := readSHA(filepath.Join(dir, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p2.Run(); err != nil {
		t.Fatal(err)
	}
	manifestAfter, err := readSHA(filepath.Join(dir, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if manifestBefore != manifestAfter {
		t.Fatal("re-run rewrote the manifest")
	}
	if res.Report.AlignmentErrorNS > int64(model.Millisecond) {
		t.Fatalf("alignment %d ns", res.Report.AlignmentErrorNS)
	}
}

func TestGraphCycleAndOverflowErrorCodes(t *testing.T) {
	// Graph cycle via the timeline package directly (pipeline uses one track).
	g := cycleGraph()
	_, err := g.Validate()
	if model.AsError(err).Code != model.ErrGraphCycle {
		t.Fatalf("got %v", err)
	}
	// Overflow via model conversion.
	_, err = model.ToNano(1<<62, 1)
	if model.AsError(err).Code != model.ErrTimeOverflow {
		t.Fatalf("got %v", err)
	}
}
