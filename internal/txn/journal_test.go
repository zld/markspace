package txn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalCommitAndReopen(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := j.Begin("stage-0"); err != nil {
		t.Fatal(err)
	}
	if err := j.WriteChecksum("stage-0", make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := j.Commit("stage-0"); err != nil {
		t.Fatal(err)
	}
	if !j.Committed("stage-0") {
		t.Fatal("commit not visible in-process")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the durable boundary must survive.
	j2, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer j2.Close()
	if !j2.Committed("stage-0") {
		t.Fatal("committed boundary lost after reopen")
	}
	// Re-beginning a committed stage is rejected.
	if err := j2.Begin("stage-0"); err == nil {
		t.Fatal("re-begin of committed stage must fail")
	}
}

func TestJournalInterruptedStageRollsBack(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	// stage-0 commits; stage-1 begins but never commits (simulated crash).
	if err := j.Begin("stage-0"); err != nil {
		t.Fatal(err)
	}
	if err := j.Commit("stage-0"); err != nil {
		t.Fatal(err)
	}
	if err := j.Begin("stage-1"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	j2, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	defer j2.Close()
	if !j2.Committed("stage-0") {
		t.Fatal("committed boundary must be preserved across crash")
	}
	if j2.Committed("stage-1") {
		t.Fatal("uncommitted stage must not appear committed")
	}
	// The replayed journal contains an explicit rollback marker.
	data := readFile(t, filepath.Join(dir, "journal.wal"))
	if !containsByte(data, entryRollback) {
		t.Fatal("rollback marker not appended during replay")
	}
	// After rollback the stage can be started again.
	if err := j2.Begin("stage-1"); err != nil {
		t.Fatalf("rolled-back stage should be restartable: %v", err)
	}
}

func TestJournalIdempotentReopen(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		j, err := OpenJournal(dir)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if !j.Committed("stage-0") {
			if err := j.Begin("stage-0"); err != nil {
				t.Fatal(err)
			}
			if err := j.Commit("stage-0"); err != nil {
				t.Fatal(err)
			}
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
	}
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if !j.Committed("stage-0") {
		t.Fatal("idempotent reopen lost the boundary")
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func containsByte(b []byte, want byte) bool {
	for _, x := range b {
		if x == want {
			return true
		}
	}
	return false
}
