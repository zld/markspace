package pipeline

import (
	"path/filepath"
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/model"
)

func demoSet(t *testing.T, dir string) *fixture.DemoSet {
	t.Helper()
	set, err := fixture.BuildDemoSet(
		filepath.Join(dir, "sample.mxf"),
		filepath.Join(dir, "sample.mp4"),
		filepath.Join(dir, "corrupt.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestNormalRunLossless(t *testing.T) {
	dir := t.TempDir()
	set := demoSet(t, dir)
	res, err := NormalRun(set, filepath.Join(dir, "out.mpkm"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code %d summary=%+v", res.ExitCode, res.Summary.Failure)
	}
	if res.Summary.ByteDiff != 0 {
		t.Fatalf("byte diff %d", res.Summary.ByteDiff)
	}
	if res.Summary.MaxTimeDeltaTick > 1 {
		t.Fatalf("max time delta %d exceeds 1 tick", res.Summary.MaxTimeDeltaTick)
	}
	if res.Summary.SampleCount == 0 || res.Summary.TrackCount != 4 {
		t.Fatalf("mapping incomplete: %+v", res.Summary)
	}
	if res.Summary.OutputSHA256 == "" {
		t.Fatal("missing output SHA-256")
	}
}

func TestNormalRunDeterministicAcrossDirectories(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	r1, err := NormalRun(demoSet(t, d1), filepath.Join(d1, "out.mpkm"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := NormalRun(demoSet(t, d2), filepath.Join(d2, "out.mpkm"))
	if err != nil {
		t.Fatal(err)
	}
	if r1.Summary.OutputSHA256 != r2.Summary.OutputSHA256 {
		t.Fatal("output SHA-256 differs across output directories")
	}
}

func TestCorruptRunReportsFirstOffset(t *testing.T) {
	dir := t.TempDir()
	set := demoSet(t, dir)
	res := CorruptRun(set, "")
	if res.ExitCode != 2 {
		t.Fatalf("want exit code 2, got %d", res.ExitCode)
	}
	if res.Summary.Failure == nil || res.Summary.Failure.Offset < 0 {
		t.Fatalf("missing locatable failure: %+v", res.Summary.Failure)
	}
	if res.Summary.Failure.Code != int(model.CodeTruncatedBox) &&
		res.Summary.Failure.Code != int(model.CodeOutOfRangeSample) {
		t.Fatalf("unexpected failure code: %+v", res.Summary.Failure)
	}
}

func TestRecoveryRunReplaysAndMatchesOracle(t *testing.T) {
	res, err := RecoveryRun(fixture.BuildRecoveryScenario())
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %+v", res.ExitCode, res.Summary.Failure)
	}
	if res.Summary.ReplayCount == 0 {
		t.Fatal("expected replay count > 0")
	}
	if res.Summary.WrapCount == 0 {
		t.Fatal("expected clock wraps")
	}
	// Seven events total (six base + one late).
	if len(res.Summary.Boundaries) != 7 {
		t.Fatalf("boundary count %d, want 7", len(res.Summary.Boundaries))
	}
}
