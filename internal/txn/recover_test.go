package txn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"medpipe/internal/fault"
)

func writeTemp(t *testing.T, tx *Transaction, content string) string {
	t.Helper()
	if err := os.WriteFile(tx.TempPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, n, ferr := HashFile(tx.TempPath)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if err := tx.Prepare(sum, n); err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestRecoverPreparedTransactionCommitsSameDigest(t *testing.T) {
	dir := t.TempDir()
	mgr, ferr := NewManager(dir)
	if ferr != nil {
		t.Fatal(ferr)
	}
	tx, ferr := mgr.Begin("ok.mp4", "input-sha", "mapping-sha")
	if ferr != nil {
		t.Fatal(ferr)
	}
	preSHA := writeTemp(t, tx, "compressed-mp4-bytes")
	mgr.Close()

	mgr2, ferr := NewManager(dir)
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer mgr2.Close()
	rec, rerr := mgr2.Recover(func(path string) *fault.Error {
		if _, err := os.Stat(path); err != nil {
			return fault.Wrap(fault.ErrOutputUnreadable, "test.verify", 0, "stat", err)
		}
		return nil
	})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rec.Action != ActionCommitted || rec.OutputSHA256 != preSHA {
		t.Fatalf("recovery=%+v preSHA=%s", rec, preSHA)
	}
	finalPath := filepath.Join(dir, "ok.mp4")
	sum, _, herr := HashFile(finalPath)
	if herr != nil || sum != preSHA {
		t.Fatalf("final hash=%s err=%v want %s", sum, herr, preSHA)
	}
	if _, err := os.Stat(finalPath + ".commit.json"); err != nil {
		t.Fatalf("commit marker missing: %v", err)
	}
	marker, ok := mgr2.CommittedMarker("ok.mp4", "input-sha", "mapping-sha")
	if !ok || marker.OutputSHA256 != preSHA {
		t.Fatalf("marker=%+v ok=%v", marker, ok)
	}
}

func TestRecoverDigestMismatchRollsBack(t *testing.T) {
	dir := t.TempDir()
	mgr, _ := NewManager(dir)
	tx, _ := mgr.Begin("bad.mp4", "in", "map")
	preSHA := writeTemp(t, tx, "original")
	if err := os.WriteFile(tx.TempPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr.Close()

	mgr2, _ := NewManager(dir)
	rec, rerr := mgr2.Recover(nil)
	if rerr == nil {
		t.Fatal("expected digest mismatch")
	}
	var fe *fault.Error
	if !errors.As(rerr, &fe) || fe.Code != fault.ErrDigestMismatch {
		t.Fatalf("recovery error=%v", rerr)
	}
	if rec.Action != ActionRollback || rec.RollbackCount != 1 {
		t.Fatalf("rec=%+v", rec)
	}
	if _, err := os.Stat(tx.TempPath); !os.IsNotExist(err) {
		t.Fatalf("temp remains after rollback: %v", err)
	}
	if _, err := os.Stat(tx.FinalPath); !os.IsNotExist(err) {
		t.Fatal("final output should not exist")
	}
	_ = preSHA
}

func TestRecoverStartedTransactionRollsBackTemp(t *testing.T) {
	dir := t.TempDir()
	mgr, _ := NewManager(dir)
	tx, _ := mgr.Begin("interrupted.mp4", "in", "map")
	if err := tx.Stage("write", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tx.TempPath, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr.Close()

	mgr2, _ := NewManager(dir)
	rec, rerr := mgr2.Recover(nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if rec.Action != ActionRollback {
		t.Fatalf("action=%s want rollback", rec.Action)
	}
	if _, err := os.Stat(tx.TempPath); !os.IsNotExist(err) {
		t.Fatal("partial temp was not removed")
	}
}
