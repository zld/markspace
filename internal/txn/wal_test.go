package txn

import (
	"os"
	"path/filepath"
	"testing"

	perr "gamepack/internal/errors"
)

func TestWALAppendAndReopen(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Record{Stage: StageScan, State: StateStarted, InputHash: "h1", ResumeAt: "0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Record{Stage: StageScan, State: StateVerified, InputHash: "h1", ResumeAt: "12"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(Record{Stage: StageAlign, State: StateStarted, ResumeAt: "12"}); err != nil {
		t.Fatal(err)
	}

	stage, rec := w.LastVerified()
	if stage != StageScan || rec.ResumeAt != "12" {
		t.Fatalf("last verified = %s rec=%v", stage, rec)
	}

	// Reopen: records must survive and sequence numbers continue.
	w2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(w2.Records()) != 3 {
		t.Fatalf("reloaded records = %d", len(w2.Records()))
	}
	r, err := w2.Append(Record{Stage: StageAlign, State: StateVerified, ResumeAt: "20"})
	if err != nil || r.Seq != 4 {
		t.Fatalf("append after reopen seq=%d err=%v", r.Seq, err)
	}
}

func TestWALRejectsBackwardTransition(t *testing.T) {
	w, _ := Open(t.TempDir())
	_, _ = w.Append(Record{Stage: StageMux, State: StateStarted})
	_, err := w.Append(Record{Stage: StageScan, State: StateStarted})
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeTxn {
		t.Fatalf("want TXN_CORRUPT for backward jump, got %v", err)
	}
}

func TestWALRollbackAllowedAndCounted(t *testing.T) {
	w, _ := Open(t.TempDir())
	_, _ = w.Append(Record{Stage: StageVerify, State: StateStarted})
	if _, err := w.Append(Record{Stage: StageMux, State: StateRolledBack}); err != nil {
		t.Fatalf("rollback transition must be allowed: %v", err)
	}
	if w.RollbackCount() != 1 {
		t.Fatalf("rollbacks = %d, want 1", w.RollbackCount())
	}
}

func TestCorruptWALRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wal.log"), []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); perr.AsError(err) == nil {
		t.Fatal("corrupt WAL must fail to open")
	}
}

func TestAtomicFileCommitAndAbort(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "manifest.json")
	if err := WriteAtomic(target, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v1" {
		t.Fatalf("content = %q", got)
	}
	// Overwrite atomically.
	if err := WriteAtomic(target, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "v2" {
		t.Fatalf("content = %q, want v2", got)
	}
	// Abort leaves the existing target intact.
	a, err := CreateAtomic(target)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = a.Write([]byte("partial"))
	a.Abort()
	got, _ = os.ReadFile(target)
	if string(got) != "v2" {
		t.Fatal("abort must not change the committed target")
	}
}

func TestCleanLeftovers(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, tempPrefix+"abc")
	keep := filepath.Join(dir, "real.json")
	if err := os.WriteFile(junk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := CleanLeftovers(dir)
	if err != nil || n != 1 {
		t.Fatalf("cleaned=%d err=%v", n, err)
	}
	if _, err := os.Stat(junk); !os.IsNotExist(err) {
		t.Fatal("leftover temp file should be removed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("non-temp file must be preserved")
	}
}

func TestTempFilePermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.bin")
	a, err := CreateAtomic(target)
	if err != nil {
		t.Fatal(err)
	}
	name := a.tmp.Name()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("temp perm = %o, want 0600", perm)
	}
	a.Abort()
}
