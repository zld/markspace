package journal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/fault"
)

func TestStageKeyChangesWithInputs(t *testing.T) {
	k1 := StageKey("inA", "params", "manifest", 1)
	if k1 == StageKey("inB", "params", "manifest", 1) {
		t.Fatal("key must change with input digest")
	}
	if k1 == StageKey("inA", "params2", "manifest", 1) {
		t.Fatal("key must change with param digest")
	}
	if k1 == StageKey("inA", "params", "manifest", 2) {
		t.Fatal("key must change with version")
	}
	if k1 != StageKey("inA", "params", "manifest", 1) {
		t.Fatal("key must be stable")
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	j, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Stages) != 0 {
		t.Fatalf("stages = %v", j.Stages)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	j := &Journal{InputDigest: "in", ParamDigest: "pa"}
	j.Put(&StageRecord{Name: "manifest", Version: 1, Key: "k1", Status: StatusVerified, OutputDigest: "d1"})
	if err := j.Save(dir); err != nil {
		t.Fatal(err)
	}
	j2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := j2.Lookup("manifest", "k1")
	if !ok || rec.OutputDigest != "d1" {
		t.Fatalf("lookup failed: %+v", j2)
	}
	if _, ok := j2.Lookup("manifest", "other-key"); ok {
		t.Fatal("lookup with wrong key must fail")
	}
	if _, ok := j2.Lookup("other-stage", "k1"); ok {
		t.Fatal("lookup with wrong stage must fail")
	}
}

func TestCorruptJournalIsResumeFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Stage != fault.StageJournal {
		t.Fatalf("got %v", err)
	}
	if code := fault.ExitCode(err); code != fault.ExitCommitPhase {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestPutReplacesAndCommit(t *testing.T) {
	j := &Journal{}
	j.Put(&StageRecord{Name: "a", Key: "k1", Status: StatusVerified})
	j.Put(&StageRecord{Name: "a", Key: "k2", Status: StatusVerified})
	if len(j.Stages) != 1 || j.Stages[0].Key != "k2" {
		t.Fatalf("put did not replace: %+v", j.Stages)
	}
	j.MarkCommitted()
	if j.Stages[0].Status != StatusCommitted {
		t.Fatalf("status = %s", j.Stages[0].Status)
	}
	j.Stages[0].Reused = true
	if j.ReusedCount() != 1 {
		t.Fatalf("reused = %d", j.ReusedCount())
	}
}
