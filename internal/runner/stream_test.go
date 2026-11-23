package runner

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/timeline"
)

func testPlan(t *testing.T) *manifest.Plan {
	t.Helper()
	points := make([]int64, 7)
	for i := range points {
		points[i] = int64(i) * 1_000_000_000
	}
	mt := &timeline.MasterTimeline{
		MasterTrack: 1, StartNS: 0, EndNS: 6_000_000_000, CommonPoints: points,
	}
	var es model.Errors
	plan, err := manifest.Build(mt, manifest.Config{
		TargetSegmentNS: 2_000_000_000, ToleranceNS: 500_000_000,
	}, &es)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testInputs() []InputRef {
	return []InputRef{{Path: "track-001.mp4", SHA256: "abc"}, {Path: "track-002.mxf", SHA256: "def"}}
}

func TestExecuteDeterministic(t *testing.T) {
	plan := testPlan(t)
	dir1, dir2 := t.TempDir(), t.TempDir()
	r1, err := Execute(plan, testInputs(), Config{WorkDir: dir1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Execute(plan, testInputs(), Config{WorkDir: dir2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.OutputSHA256 != r2.OutputSHA256 || r1.ManifestSHA256 != r2.ManifestSHA256 {
		t.Fatal("two independent runs must be byte-identical")
	}
	if len(r1.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(r1.Segments))
	}
}

func TestResumeReusesCheckpoint(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	cfg := Config{WorkDir: dir}
	first, err := Execute(plan, testInputs(), cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Execute(plan, testInputs(), cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CheckpointReused {
		t.Fatal("second run must reuse the verified checkpoint")
	}
	if second.ResumedFrom != len(plan.Segments) {
		t.Fatalf("resumed from %d, want %d", second.ResumedFrom, len(plan.Segments))
	}
	if second.OutputSHA256 != first.OutputSHA256 {
		t.Fatal("resumed output must match original")
	}
}

func TestResumeFromPartialCheckpoint(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	cfg := Config{WorkDir: dir}
	full, err := Execute(plan, testInputs(), cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Rewind checkpoint to claim only the first segment is done.
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	cp.Completed = cp.Completed[:1]
	cp.NextSegment = 1
	deleteFinalHashes(cp)
	if err := SaveCheckpoint(dir, cp); err != nil {
		t.Fatal(err)
	}
	// Remove the later segment outputs so they must be regenerated.
	for _, seg := range full.Segments[1:] {
		if err := os.Remove(seg.Path); err != nil {
			t.Fatal(err)
		}
	}
	resumed, err := Execute(plan, testInputs(), cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.CheckpointReused || resumed.ResumedFrom != 1 {
		t.Fatalf("must resume at segment 1: reused=%v from=%d",
			resumed.CheckpointReused, resumed.ResumedFrom)
	}
	if resumed.OutputSHA256 != full.OutputSHA256 {
		t.Fatal("rebuilt output must match full run")
	}
}

func TestCorruptCheckpointRollsBackToZero(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	full, err := Execute(plan, testInputs(), Config{WorkDir: dir}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper the checkpoint JSON without updating its checksum file.
	cpPath := checkpointPath(dir)
	if err := os.WriteFile(cpPath, []byte("{tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Execute(plan, testInputs(), Config{WorkDir: dir}, 0)
	if err != nil {
		t.Fatalf("corrupt checkpoint must self-heal, got %v", err)
	}
	if r.CheckpointReused {
		t.Fatal("tampered checkpoint must not be trusted")
	}
	if r.ResumedFrom != 0 || r.OutputSHA256 != full.OutputSHA256 {
		t.Fatalf("must redo from safe boundary 0, from=%d", r.ResumedFrom)
	}
}

func TestCorruptSegmentRollsBackToSafePrefix(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	full, err := Execute(plan, testInputs(), Config{WorkDir: dir}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the middle segment output; checkpoint still claims it is good.
	target := full.Segments[1].Path
	if err := os.WriteFile(target, []byte("corrupted-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Execute(plan, testInputs(), Config{WorkDir: dir}, 0)
	if err != nil {
		t.Fatalf("must recover from bad segment: %v", err)
	}
	if r.OutputSHA256 != full.OutputSHA256 {
		t.Fatal("output after segment recovery must match original")
	}
	if r.CheckpointReused {
		t.Fatal("a failed verification invalidates reuse")
	}
	if r.ResumedFrom > 1 {
		t.Fatalf("safe prefix cannot exceed the verified segment count, got %d", r.ResumedFrom)
	}
}

func TestInputChangeInvalidatesCheckpoint(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	if _, err := Execute(plan, testInputs(), Config{WorkDir: dir}, 0); err != nil {
		t.Fatal(err)
	}
	changed := []InputRef{{Path: "track-001.mp4", SHA256: "zzz"}, {Path: "track-002.mxf", SHA256: "def"}}
	r, err := Execute(plan, changed, Config{WorkDir: dir}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.CheckpointReused || r.ResumedFrom != 0 {
		t.Fatal("changed input digest must force a full rescan/redo")
	}
}

func TestSegmentLimitEnforced(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	_, err := Execute(plan, testInputs(), Config{WorkDir: dir, MaxSegments: 2}, 0)
	pe, ok := model.AsPipeline(err)
	if !ok || pe.Code != model.CodeLimitExceeded {
		t.Fatalf("want limit_exceeded, got %v", err)
	}
}

func TestNoManifestOnFailure(t *testing.T) {
	plan := testPlan(t)
	dir := t.TempDir()
	if _, err := Execute(plan, testInputs(), Config{WorkDir: dir, MaxSegments: 1}, 0); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("a failed run must not leave a seemingly complete manifest")
	}
}

func deleteFinalHashes(cp *Checkpoint) {
	cp.OutputSHA256 = ""
	cp.ManifestSHA256 = ""
}
