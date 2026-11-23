package pipeline

import (
	"path/filepath"
	"reflect"
	"testing"

	"mediaverify/internal/bitrate"
	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
	"mediaverify/internal/fixture"
)

func baseConfig(t *testing.T, out string) Config {
	t.Helper()
	return Config{
		Seed:       7,
		Duration:   clock.Nanoseconds(20_000_000_000),
		Segment:    clock.Nanoseconds(2_000_000_000),
		Ladder:     []int64{400_000, 900_000, 1_800_000},
		LateWindow: clock.Nanoseconds(750_000_000),
		Fault:      fixture.FaultNone,
		FrameRate:  clock.FrameRate25(),
		OutPath:    out,
	}
}

func TestDeterministicHashAcrossRuns(t *testing.T) {
	r1, err := Run(baseConfig(t, ""))
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	r2, err := Run(baseConfig(t, ""))
	if err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if r1.ManifestHash != r2.ManifestHash {
		t.Fatalf("manifest hash differs across identical runs: %s vs %s",
			r1.ManifestHash, r2.ManifestHash)
	}
	if !reflect.DeepEqual(r1.Report, r2.Report) {
		t.Fatalf("quality report differs across identical runs")
	}
	if r1.Lost != r2.Lost || r1.Reordered != r2.Reordered || r1.Duplicate != r2.Duplicate {
		t.Fatal("packet statistics differ across identical runs")
	}
}

func TestRenditionCutsAndBitrateInvariants(t *testing.T) {
	res, err := Run(baseConfig(t, ""))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if res.Report.AlignmentMaxFrame > 1 {
		t.Fatalf("alignment %d frames exceeds one-frame bound", res.Report.AlignmentMaxFrame)
	}
	if res.Report.MaxDeviationPct > bitrate.MaxDeviationPct {
		t.Fatalf("bitrate deviation %.2f%% exceeds 15%%", res.Report.MaxDeviationPct)
	}
	if res.Report.BitrateErrorPct > bitrate.MaxDeviationPct {
		t.Fatalf("active rung deviation %.2f%% exceeds 15%%", res.Report.BitrateErrorPct)
	}
	if res.Report.Segments != 10 {
		t.Fatalf("want 10 segments for 20s/2s, got %d", res.Report.Segments)
	}
	if res.SubtitleSpans == 0 {
		t.Fatal("subtitle spans were not associated to segments")
	}
}

func TestBurstLossRecoveryAndUntrustedIntervals(t *testing.T) {
	cfg := baseConfig(t, "")
	cfg.Fault = fixture.FaultBurstLoss
	res, err := Run(cfg)
	if err != nil {
		t.Fatalf("burst-loss run should degrade, not fail: %v", err)
	}
	if res.Lost == 0 {
		t.Fatal("burst loss produced no lost packets")
	}
	if res.Report.DegradedIntervals == 0 {
		t.Fatal("burst loss produced no untrusted intervals")
	}
	if res.Report.RecoveredSegments == 0 {
		t.Fatal("slicing never recovered at a valid keyframe")
	}
}

func TestFailedStageDoesNotPolluteOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	cfg := baseConfig(t, path)
	cfg.Fault = fixture.FaultEmptySubtitle
	_, err := Run(cfg)
	if errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("empty subtitle must be KindInvalidInput (exit 2), got %v", err)
	}
	if len(mustGlob(t, filepath.Dir(path))) != 0 {
		t.Fatal("failed stage left output files behind; sealed output was polluted")
	}
}

func mustGlob(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestConfigValidationExitClasses(t *testing.T) {
	cfg := baseConfig(t, "")
	cfg.Duration = 3 * clock.Nanoseconds(3600_000_000_000)
	if _, err := Run(cfg); errs.Class(err) != errs.KindLimitExceeded {
		t.Fatalf("2-hour limit must be KindLimitExceeded, got %v", err)
	}
	cfg = baseConfig(t, "")
	cfg.Ladder = []int64{900_000}
	if _, err := Run(cfg); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("single rung must be KindInvalidInput, got %v", err)
	}
}
