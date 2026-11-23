package txn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacheck/internal/model"
)

func samplePayload(seed int) map[string]interface{} {
	return map[string]interface{}{
		"seed": seed,
		"metrics": map[string]interface{}{
			"quality_mean": 0.91,
			"alignment_ms": 3,
		},
	}
}

func TestCommitCreatesRecord(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(j, dir)
	committed, err := mgr.Run("t1", samplePayload(1), nil)
	if err != nil || !committed {
		t.Fatalf("commit: committed=%v err=%v", committed, err)
	}
	path, _ := mgr.FilePath("t1")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed record missing: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStageFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	mgr := NewManager(j, dir)
	hooks := map[model.Stage]StageFunc{
		model.StageValidate: func(map[string]interface{}) error {
			return errors.New("validate rejected")
		},
	}
	_, err := mgr.Run("t1", samplePayload(1), hooks)
	if !errors.Is(err, model.ErrStageFailed) {
		t.Fatalf("want stage failure, got %v", err)
	}
	if j.IsCommitted("t1") {
		t.Fatal("failed transaction must not be committed")
	}
	path, _ := mgr.FilePath("t1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("no committed file may exist after rollback")
	}
	// A later valid transaction still commits: boundary preserved.
	ok, err := mgr.Run("t2", samplePayload(2), nil)
	if err != nil || !ok {
		t.Fatalf("transaction after rollback: %v %v", ok, err)
	}
}

func TestReplayNoDoubleCommit(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	mgr := NewManager(j, dir)
	if _, err := mgr.Run("t1", samplePayload(1), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Run("t2", samplePayload(2), nil); err != nil {
		t.Fatal(err)
	}
	j.Close()

	// Reopen: replay must report each committed id exactly once.
	j2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr2 := NewManager(j2, dir)
	ids := j2.CommittedIDs()
	if len(ids) != 2 || ids[0] != "t1" || ids[1] != "t2" {
		t.Fatalf("replayed committed ids wrong: %v", ids)
	}
	// Re-running an already committed txn is a no-op (no double commit).
	committed, err := mgr2.Run("t1", samplePayload(1), nil)
	if err != nil || committed {
		t.Fatalf("replay re-commit: committed=%v err=%v", committed, err)
	}
}

func TestCrashAfterPrepareRecovers(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	// Simulate a crash between WAL prepare and commit marker.
	if err := j.Append(&Entry{
		TxnID: "orphan", Stage: model.StageParse, State: StatePrepared,
		Payload: samplePayload(9),
	}); err != nil {
		t.Fatal(err)
	}
	j.Close()

	j2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(j2, dir)
	committed, rolledBack, err := mgr.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolledBack) != 1 || rolledBack[0] != "orphan" {
		t.Fatalf("rollback set wrong: %v", rolledBack)
	}
	if len(committed) != 0 {
		t.Fatalf("orphan txn must not commit: %v", committed)
	}
}

func TestReplaySummaryStable(t *testing.T) {
	dir := t.TempDir()
	run := func() string {
		j, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		mgr := NewManager(j, dir)
		for _, id := range []string{"a", "b", "c"} {
			if _, err := mgr.Run(id, samplePayload(len(id)), nil); err != nil {
				t.Fatal(err)
			}
		}
		j.Close()
		// Summary = ordered concatenation of committed record hashes.
		return committedHash(t, dir)
	}
	first := run()
	second := run()
	if first != second {
		t.Fatalf("replay changed summary: %s != %s", first, second)
	}
}

func TestCorruptJournalRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, journalFileName)
	if err := os.WriteFile(path, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, model.ErrCorruptJournal) {
		t.Fatalf("want corrupt journal error, got %v", err)
	}
}

func TestUnsafeJournalPath(t *testing.T) {
	if _, err := Open(""); !errors.Is(err, model.ErrUnsafePath) {
		t.Fatalf("want unsafe path, got %v", err)
	}
}

func committedHash(t *testing.T, dir string) string {
	t.Helper()
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var sum string
	for _, id := range j.CommittedIDs() {
		e := j.entries[id]
		sum += e.ContentHash
	}
	return sum
}
