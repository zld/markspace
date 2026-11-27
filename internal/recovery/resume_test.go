package recovery

import (
	"os"
	"path/filepath"
	"testing"

	"gamepack/internal/txn"
)

func seedWAL(t *testing.T, dir string, recs ...txn.Record) *txn.WAL {
	t.Helper()
	w, err := txn.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if _, err := w.Append(r); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}
	return w
}

func TestInspectFreshJob(t *testing.T) {
	plan, err := Inspect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Fresh || plan.ResumeStage != txn.StageScan {
		t.Fatalf("fresh plan = %+v", plan)
	}
}

func TestResumeAfterVerifiedScan(t *testing.T) {
	dir := t.TempDir()
	seedWAL(t, dir,
		txn.Record{Stage: txn.StageScan, State: txn.StateStarted},
		txn.Record{Stage: txn.StageScan, State: txn.StateVerified, ResumeAt: "42"},
	)
	plan, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResumeStage != txn.StageAlign || plan.VerifiedStage != txn.StageScan {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.ResumeAt != "42" {
		t.Fatalf("resume position = %q, want 42", plan.ResumeAt)
	}
}

func TestResumeAfterCrashMidMux(t *testing.T) {
	dir := t.TempDir()
	seedWAL(t, dir,
		txn.Record{Stage: txn.StageScan, State: txn.StateVerified, ResumeAt: "10"},
		txn.Record{Stage: txn.StageAlign, State: txn.StateVerified, ResumeAt: "20"},
		txn.Record{Stage: txn.StageMux, State: txn.StateStarted},
	)
	plan, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Mux never verified: resume from mux, not the later verify stage.
	if plan.ResumeStage != txn.StageMux {
		t.Fatalf("resume stage = %s, want mux", plan.ResumeStage)
	}
}

func TestResumeAfterFailureRestartsFailedStage(t *testing.T) {
	dir := t.TempDir()
	seedWAL(t, dir,
		txn.Record{Stage: txn.StageScan, State: txn.StateVerified},
		txn.Record{Stage: txn.StageAlign, State: txn.StateFailed, ResumeAt: "20"},
	)
	plan, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResumeStage != txn.StageAlign {
		t.Fatalf("resume stage = %s, want align", plan.ResumeStage)
	}
}

func TestCommittedJobReportsCommit(t *testing.T) {
	dir := t.TempDir()
	seedWAL(t, dir,
		txn.Record{Stage: txn.StageCommit, State: txn.StateVerified},
	)
	plan, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResumeStage != txn.StageCommit {
		t.Fatalf("committed plan = %+v", plan)
	}
}

func TestInspectCleansLeftovers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".packtmp-xyz"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Leftovers != 1 {
		t.Fatalf("leftovers = %d, want 1", plan.Leftovers)
	}
	if _, err := os.Stat(filepath.Join(dir, ".packtmp-xyz")); !os.IsNotExist(err) {
		t.Fatal("crash leftover must be cleaned")
	}
}

func TestMarkRollbackAndOutputRemoval(t *testing.T) {
	dir := t.TempDir()
	segDir := filepath.Join(dir, "segments")
	if err := os.MkdirAll(segDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "a.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := seedWAL(t, dir, txn.Record{Stage: txn.StageMux, State: txn.StateStarted})
	if err := MarkRollback(w, txn.StageMux, "20"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveStageOutputs(segDir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(segDir); len(entries) != 0 {
		t.Fatal("rolled-back segment outputs must be removed")
	}
	if w.RollbackCount() != 1 {
		t.Fatalf("rollback count = %d", w.RollbackCount())
	}
}
