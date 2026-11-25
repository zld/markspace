package main

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/gen"
	"mediad/internal/pipeline"
	"mediad/internal/timeline"
)

func TestRunDemoAllScenariosReportsContract(t *testing.T) {
	dir := t.TempDir()
	rep, code := runDemo(dir, "all")
	if code != 0 {
		t.Fatalf("combined demo must exit 0, got %d", code)
	}
	if rep.ErrorCode != timeline.ErrBoxTruncated {
		t.Fatalf("corrupt scenario must be located, got code %q", rep.ErrorCode)
	}
	if rep.ErrorFile != "corrupt.m4s" {
		t.Fatalf("error file = %q", rep.ErrorFile)
	}
	if !rep.RTPWrapMonotonic || !rep.TimelineMonotonic {
		t.Fatalf("boundary timeline must stay monotonic: %+v", rep)
	}
	if rep.RecoveryMS < 0 || rep.RecoveryMS >= 5000 {
		t.Fatalf("recovery_ms out of range: %d", rep.RecoveryMS)
	}
	if rep.ManifestSHA256 == "" || rep.ManifestSHA256 != rep.OutputSHA256 {
		t.Fatalf("checksums missing or mismatched: %+v", rep)
	}
	if rep.CorruptExitCode != 2 {
		t.Fatalf("corrupt exit code = %d, want 2", rep.CorruptExitCode)
	}
}

func TestRunDemoCorruptScenarioExitsTwo(t *testing.T) {
	_, code := runDemo(t.TempDir(), "corrupt")
	if code != 2 {
		t.Fatalf("corrupt scenario exit = %d, want 2", code)
	}
}

func TestNormalProducesTwoSegmentsAndSilence(t *testing.T) {
	res, err := runNormal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) < 2 {
		t.Fatalf("want >=2 segments, got %d", len(res.Segments))
	}
	if len(res.SilenceIntervals) < 1 {
		t.Fatalf("want >=1 silence interval, got %d", len(res.SilenceIntervals))
	}
}

func TestCorruptCutLocatedWithinOneByte(t *testing.T) {
	_, want := gen.TruncatedGroup()
	me := runCorrupt(t.TempDir())
	if me == nil || me.Code != timeline.ErrBoxTruncated {
		t.Fatalf("want truncation error, got %+v", me)
	}
	diff := me.Offset - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 1 {
		t.Fatalf("cut at %d, actual %d (±1 required)", me.Offset, want)
	}
}

// TestRecoveryMismatchExitThree exercises the exit-3 path end to end: a
// committed manifest whose checksum no longer matches must be refused.
func TestRecoveryMismatchExitThree(t *testing.T) {
	dir := t.TempDir()
	data := gen.NormalGroup()
	in := []pipeline.InputFile{{Name: "normal.m4s", Data: data}}
	cfg := baseConfig(dir)
	if _, err := pipeline.Run(in, cfg); err != nil {
		t.Fatal(err)
	}
	// Corrupt the committed manifest, then resume with the same input.
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{\"tampered\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := pipeline.Run(in, baseConfig(dir))
	me := timeline.AsMediaError(err)
	if me == nil || me.Code != timeline.ErrRecovery {
		t.Fatalf("want ErrRecovery, got %v", err)
	}
	if timeline.ExitCode(err) != 3 {
		t.Fatalf("recovery mismatch exit = %d, want 3", timeline.ExitCode(err))
	}
}

func TestBoundaryRecoveryReproducesChecksum(t *testing.T) {
	resumed, mono, err := runBoundary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if resumed == nil {
		t.Fatal("resumed result required")
	}
	if !mono {
		t.Fatal("boundary timeline not monotonic")
	}
	if resumed.RecoveryMS >= 5000 {
		t.Fatalf("recovery_ms = %d exceeds 5000", resumed.RecoveryMS)
	}
}
