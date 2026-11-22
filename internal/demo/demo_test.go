package demo

import (
	"os"
	"path/filepath"
	"testing"
)

func runCase(t *testing.T, name string) *Report {
	t.Helper()
	dir := t.TempDir()
	rep, code, err := Run(Options{Case: name, OutDir: dir})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if rep == nil {
		t.Fatalf("%s: nil report", name)
	}
	if code != 0 {
		t.Fatalf("%s: exit code %d (failures %+v)", name, code, rep.Failures)
	}
	// Damaged-material cases record the observed failure but still exit 0
	// because the repaired rerun verified; only clean-class cases must be
	// failure-free.
	if name != CaseCorrupt && name != CaseTruncated && len(rep.Failures) != 0 {
		t.Fatalf("%s: unexpected failures %+v", name, rep.Failures)
	}
	required := []string{"report.json"}
	if name != CasePackets {
		required = append(required, "frames.bin", "index.json", "manifest.json")
	}
	for _, want := range required {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("%s: missing %s", name, want)
		}
	}
	return rep
}

func TestCleanCaseMeetsAcceptance(t *testing.T) {
	rep := runCase(t, CaseClean)
	if rep.Frames != 1200 || rep.Segments != 12 || rep.SilenceMS != 2400 || rep.DuplicateGroups != 3 {
		t.Fatalf("summary mismatch: %+v", rep)
	}
	if rep.TargetKBPS != 900 || rep.ActualKBPS != 870 || rep.DeviationPct != 3.33 {
		t.Fatalf("rate mismatch: target=%d actual=%d dev=%.2f", rep.TargetKBPS, rep.ActualKBPS, rep.DeviationPct)
	}
	if rep.AlignmentMS > 2 {
		t.Fatalf("alignment=%d > 2", rep.AlignmentMS)
	}
}

func TestCleanCaseDeterministic(t *testing.T) {
	a, b := runCase(t, CaseClean), runCase(t, CaseClean)
	if a.ManifestDigest != b.ManifestDigest {
		t.Fatalf("manifest digests differ: %s vs %s", a.ManifestDigest, b.ManifestDigest)
	}
}

func TestBurstLossCase(t *testing.T) {
	rep := runCase(t, CaseBurstLoss)
	if rep.MissingFrames != 50 {
		t.Fatalf("missing_frames=%d want 50", rep.MissingFrames)
	}
	if rep.GapCount == 0 {
		t.Fatal("expected gaps")
	}
	if rep.Frames != 1150 {
		t.Fatalf("frames=%d want 1150", rep.Frames)
	}
	if rep.ActualKBPS == 900 {
		t.Fatal("burst loss should reduce actual bitrate")
	}
}

func TestCorruptAndTruncatedRecover(t *testing.T) {
	for _, name := range []string{CaseCorrupt, CaseTruncated} {
		rep := runCase(t, name)
		if !rep.Recovered {
			t.Fatalf("%s: did not report recovery", name)
		}
		if rep.DamageOffset <= 0 {
			t.Fatalf("%s: no damage offset", name)
		}
		dir := rep.OutputDir
		// Damaged material is preserved and must not verify.
		for _, f := range []string{"frames.bin", "index.json", "manifest.json"} {
			if _, err := os.Stat(filepath.Join(dir, "damaged", f)); err != nil {
				t.Fatalf("%s: missing damaged/%s", name, f)
			}
		}
	}
}

func TestWrapCase(t *testing.T) {
	rep := runCase(t, CaseWrap)
	found := false
	for _, n := range rep.Notes {
		if len(n) > 0 && n[:11] == "wrap_events" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing wrap note: %v", rep.Notes)
	}
}

func TestDigestShiftRebuilds(t *testing.T) {
	rep := runCase(t, CaseDigestShift)
	if rep.IndexCache == nil || !rep.IndexCache.Rebuilt {
		t.Fatalf("cache was not rebuilt: %+v", rep.IndexCache)
	}
}

func TestPacketsCase(t *testing.T) {
	rep := runCase(t, CasePackets)
	if rep.Duplicates == 0 || rep.Late == 0 || rep.GapCount == 0 {
		t.Fatalf("dups=%d late=%d gaps=%d", rep.Duplicates, rep.Late, rep.GapCount)
	}
}

func TestUnknownCaseRejected(t *testing.T) {
	_, code, err := Run(Options{Case: "nope", OutDir: t.TempDir()})
	if err == nil || code != 2 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestSmallFrameOverrideScalesFeatures(t *testing.T) {
	dir := t.TempDir()
	rep, code, err := Run(Options{Case: CaseClean, OutDir: dir, Frames: 120})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if rep.Frames != 120 {
		t.Fatalf("frames=%d want 120", rep.Frames)
	}
	if rep.SilenceMS == 0 || rep.DuplicateGroups != 3 {
		t.Fatalf("scaled features missing: silence=%d groups=%d", rep.SilenceMS, rep.DuplicateGroups)
	}
	if len(rep.Failures) != 0 {
		t.Fatalf("failures=%+v", rep.Failures)
	}
}
