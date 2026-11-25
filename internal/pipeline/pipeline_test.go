package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/fixture"
	"tracecheck/internal/rtptrace"
)

func validSpec() fixture.MediaSpec {
	return fixture.MediaSpec{
		Brand: "isom",
		Tracks: []fixture.TrackSpec{
			{TrackID: 1, Timescale: 48000, SampleCount: 12, SampleDelta: 480,
				PayloadSize: 8, SyncInterval: 4, HandlerType: "soun"},
			{TrackID: 2, Timescale: 24000, SampleCount: 12, SampleDelta: 240,
				PayloadSize: 8, SyncInterval: 4, HandlerType: "vide"},
		},
	}
}

func writeInput(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "input.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStateOnlyMovesForward(t *testing.T) {
	s := NewState()
	if err := s.Advance(StageRecover); err == nil {
		t.Fatal("accepted out-of-order first transition")
	}
	for _, stage := range AllStages() {
		if err := s.Advance(stage); err != nil {
			t.Fatalf("advance %s: %v", stage, err)
		}
		s.Complete(stage)
	}
	if s.Transitions() != 6 {
		t.Fatalf("transitions = %d, want 6", s.Transitions())
	}
	if err := s.Advance(StageScan); err == nil {
		t.Fatal("accepted backwards transition")
	}
}

func TestRunCleanInputCompletesAllStages(t *testing.T) {
	f := fixture.BuildFile(validSpec())
	in := writeInput(t, f.Data)
	out := t.TempDir()
	plan := rtptrace.Plan{Seed: 7, LossRate: 0.2, DuplicateAt: []int{2}, ReorderAt: []int{4}}
	rep, err := Run(in, out, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.StageFailure != nil {
		t.Fatalf("unexpected failure: %+v", rep.StageFailure)
	}
	if rep.Transitions != 6 {
		t.Fatalf("transitions = %d, want 6", rep.Transitions)
	}
	if !rep.Verification.OK() {
		t.Fatalf("verification: %v", rep.Verification.Errors)
	}
	if rep.MaxBoundaryDelta > 1 {
		t.Fatalf("boundary delta = %d", rep.MaxBoundaryDelta)
	}
	if _, err := os.Stat(filepath.Join(out, CheckpointFile)); err != nil {
		t.Fatalf("checkpoint not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
}

func TestRerunIsDeterministicAndReusesDigest(t *testing.T) {
	f := fixture.BuildFile(validSpec())
	in := writeInput(t, f.Data)
	out := t.TempDir()
	plan := rtptrace.Plan{Seed: 11, LossRate: 0.15}
	first, err := Run(in, out, plan)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := Run(in, out, plan)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if first.Manifest.SegmentDigest != second.Manifest.SegmentDigest {
		t.Fatal("manifest digest changed on rerun")
	}
	if d := diffManifests(first, second); d != 0 {
		t.Fatalf("rerun diff = %d, want 0", d)
	}
}

func TestCheckpointRejectsDifferentDigest(t *testing.T) {
	dir := t.TempDir()
	cp := Checkpoint{InputDigest: "aaa", CurrentStage: StageScan, Transitions: 1}
	if err := Save(dir, cp); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, "bbb")
	var mismatch *clock.ErrDigestMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("want digest mismatch, got %v", err)
	}
}

func TestRunUnrecoverableInputReportsStageAndOffset(t *testing.T) {
	spec := validSpec()
	spec.Tracks[0].SyncInterval = 0
	f := fixture.BuildFile(spec)
	bad := f.CorruptSample(1, 11)
	in := writeInput(t, bad)
	out := t.TempDir()
	rep, err := Run(in, out, rtptrace.Plan{Seed: 1})
	if err == nil {
		t.Fatal("expected non-nil error for unrecoverable input")
	}
	if rep.StageFailure == nil || rep.StageFailure.Stage != StageRecover {
		t.Fatalf("failure not pinned to recover stage: %+v", rep.StageFailure)
	}
	if int64(rep.StageFailure.Offset) != int64(f.TrackMdat[1][11].Offset) {
		t.Fatalf("failure offset = %d, want %d",
			rep.StageFailure.Offset, f.TrackMdat[1][11].Offset)
	}
}

func TestCheckpointRestoresCompletedStages(t *testing.T) {
	s := NewState()
	for _, stage := range AllStages()[:3] {
		if err := s.Advance(stage); err != nil {
			t.Fatal(err)
		}
		s.Complete(stage)
	}
	cp := s.snapshot("digest-x", 4096)
	restored := Restore(&cp)
	if !restored.IsFinished(StageRecover) || restored.IsFinished(StageVerify) {
		t.Fatal("restored stage set wrong")
	}
	if restored.Current() != StageRecover || restored.Transitions() != 3 {
		t.Fatalf("restored position wrong: %s %d", restored.Current(), restored.Transitions())
	}
}

func diffManifests(a, b *Report) int {
	count := 0
	if len(a.Manifest.Segments) != len(b.Manifest.Segments) {
		return -1
	}
	for i := range a.Manifest.Segments {
		if a.Manifest.Segments[i].Start != b.Manifest.Segments[i].Start {
			count++
		}
	}
	return count
}
