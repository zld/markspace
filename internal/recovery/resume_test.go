package recovery_test

import (
	"path/filepath"
	"testing"

	"archivecheck/internal/fixture"
	"archivecheck/internal/model"
	mp4scan "archivecheck/internal/mp4"
	"archivecheck/internal/plan"
	"archivecheck/internal/recovery"
	"archivecheck/internal/store"
)

func TestJournalResumeUsesIndependentLayerCursors(t *testing.T) {
	j := store.NewJournal("rec", "input-hash", model.Parameters{Bitrates: []int64{400_000, 900_000}})
	if err := j.Transition(plan.StageScanned, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Transition(plan.StagePlanned, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.Transition(plan.StageProcessing, ""); err != nil {
		t.Fatal(err)
	}
	low := j.Layer(400_000)
	if err := low.RecordFragment(0, "hash-0"); err != nil {
		t.Fatal(err)
	}
	if err := low.RecordFragment(1, "hash-1"); err != nil {
		t.Fatal(err)
	}
	high := j.Layer(900_000)
	if err := high.RecordFragment(0, "high-0"); err != nil {
		t.Fatal(err)
	}

	r := recovery.FromJournal(j)
	if !r.Found || r.StartStage != plan.StageProcessing {
		t.Fatalf("unexpected resume: %+v", r)
	}
	if got := recovery.LayerCursor(j, 400_000); got != 2 {
		t.Fatalf("low cursor=%d want2", got)
	}
	if got := recovery.LayerCursor(j, 900_000); got != 1 {
		t.Fatalf("high cursor=%d want1 (other layer overwritten)", got)
	}
	if got := r.LayerCursors[900_000]; got != 1 {
		t.Fatalf("resume cursor=%d want1", got)
	}

	// Re-delivering a committed fragment with the same hash is idempotent.
	if err := high.RecordFragment(0, "high-0"); err != nil {
		t.Fatalf("idempotent redelivery failed: %v", err)
	}
	if err := high.RecordFragment(0, "changed"); err == nil {
		t.Fatal("expected changed committed hash to fail")
	}
}

func TestRecoveryPositionUsesPrecedingKeyframe(t *testing.T) {
	dir := t.TempDir()
	paths, err := fixture.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := fixture.LoadManifest(paths.ValidInput)
	if err != nil {
		t.Fatal(err)
	}
	a := m.Assets[0]
	a.Path = filepath.Join(dir, a.Path)
	scan, err := mp4scan.ScanAsset(a)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := recovery.Position(scan, 900_000, 1, fixture.TargetBoundary)
	if err != nil {
		t.Fatal(err)
	}
	if pos.RequestTick != 36_000 || pos.ActualTick != 30_000 || pos.DeltaTicks != 6_000 {
		t.Fatalf("unexpected position: %+v", pos)
	}
	request := recovery.NextRequestTick(a.EditIntervals[0], 1, 2)
	if request != fixture.TargetBoundary {
		t.Fatalf("request=%d want %d", request, fixture.TargetBoundary)
	}
}

func TestFailedJournalRetryTransition(t *testing.T) {
	j := store.NewJournal("r", "h", model.Parameters{})
	_ = j.Transition(plan.StageScanned, "")
	if err := j.Transition(plan.StageFailed, "test"); err != nil {
		t.Fatal(err)
	}
	ok, err := recovery.RetryFailed(j)
	if err != nil || !ok {
		t.Fatalf("RetryFailed ok=%v err=%v", ok, err)
	}
	if j.Stage != plan.StagePendingScan {
		t.Fatalf("stage=%s want pending_scan", j.Stage)
	}
}
