package commit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediq/internal/journal"
)

func openWAL(t *testing.T, dir string) *journal.WAL {
	t.Helper()
	w, err := journal.Open(dir)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	return w
}

func TestAtomicCommitAndRehash(t *testing.T) {
	dir := t.TempDir()
	w := openWAL(t, dir)
	final := filepath.Join(dir, "out", "manifest.json")
	sw, err := Create(final)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Write([]byte(`{"segments":24}`)); err != nil {
		t.Fatal(err)
	}
	hash, err := sw.Commit(w, "manifest", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(final)
	if err != nil || got != hash {
		t.Fatalf("committed hash mismatch: %v %s != %s", err, got, hash)
	}
	// No staging files survive a commit.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "out", TempPrefix+"*")); len(leftovers) != 0 {
		t.Fatalf("temp files leaked: %v", leftovers)
	}
	if err := journal.VerifyCommitHash(dir, "manifest", HashFile); err != nil {
		t.Fatalf("commit hash verification: %v", err)
	}
}

func TestAbortLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	w := openWAL(t, dir)
	final := filepath.Join(dir, "segments", "s1.bin")
	sw, err := Create(final)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := sw.Abort(w, "segment"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatal("aborted output must not exist")
	}
	rep, err := journal.Analyze(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.IsCommitted("segment") {
		t.Fatal("aborted stage must not read as committed")
	}
}

func TestRecoverRollsBackPendingStage(t *testing.T) {
	dir := t.TempDir()
	w := openWAL(t, dir)
	tmp := filepath.Join(dir, ".mediq-tmp-pending")
	if err := os.WriteFile(tmp, []byte("staged but uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Begin("segment", "input-hash", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Prepared("segment", "plan-hash", "out-hash", []string{tmp}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Crash before committed: recovery must remove the staged file.
	rep, err := journal.Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RolledBack != 1 || rep.Replayed != 0 {
		t.Fatalf("recover = %+v, want one rollback", rep)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("pending temp file must be removed by recovery")
	}
	rep2, err := journal.Analyze(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range rep2.Stages {
		if st.Stage == "segment" && st.State != journal.StateRolledBack {
			t.Fatalf("segment state = %s, want rolled_back", st.State)
		}
	}
}

func TestCommittedStageSurvivesRecoveryAndRejectsDrift(t *testing.T) {
	dir := t.TempDir()
	w := openWAL(t, dir)
	final := filepath.Join(dir, "manifest.json")
	sw, err := Create(final)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Write([]byte("durable")); err != nil {
		t.Fatal(err)
	}
	hash, err := sw.Commit(w, "manifest", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second, pending stage exists at crash time.
	tmp := filepath.Join(dir, ".mediq-tmp-other")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Prepared("report", "p", "h", []string{tmp}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err := journal.Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Replayed != 1 || !rep.IsCommitted("manifest") {
		t.Fatalf("committed stage must survive: %+v", rep)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("committed file must remain: %v", err)
	}
	if got, _ := HashFile(final); got != hash {
		t.Fatal("committed content changed during recovery")
	}
	// Tamper with the committed file: hash verification must fail.
	if err := os.WriteFile(final, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := journal.VerifyCommitHash(dir, "manifest", HashFile); err == nil ||
		!strings.Contains(err.Error(), "hash drifted") {
		t.Fatalf("want hash-drift recovery failure, got %v", err)
	}
}

func TestWALSequenceMonotonic(t *testing.T) {
	dir := t.TempDir()
	w := openWAL(t, dir)
	for i := 0; i < 5; i++ {
		if _, err := w.Begin("scan", "h", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening resumes the sequence instead of resetting it.
	w2 := openWAL(t, dir)
	rec, err := w2.Begin("plan", "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 6 {
		t.Fatalf("seq after reopen = %d, want 6", rec.Seq)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
}
