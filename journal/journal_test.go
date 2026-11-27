package journal

import (
	"path/filepath"
	"testing"
)

func TestCommitAndIdempotentRerun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	j, rb, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rb != nil {
		t.Fatal("fresh journal should not roll back")
	}
	rec := Record{Key: "p/video/0", ProgramKey: "p", TrackID: "video",
		StartNS: 0, EndNS: 1_000_000_000, Source: "v/1.m4s", Digest: "aa"}
	ok, err := j.Commit(rec)
	if err != nil || !ok {
		t.Fatalf("first commit: ok=%v err=%v", ok, err)
	}
	// Same key again: no-op, no duplicate.
	ok, err = j.Commit(rec)
	if err != nil || ok {
		t.Fatalf("rerun commit should be no-op: ok=%v err=%v", ok, err)
	}
	if !j.IsCommitted("p/video/0") {
		t.Fatal("committed key missing")
	}
	if got := j.Snapshot(); len(got.Committed) != 1 || got.WatermarkNS != 1_000_000_000 {
		t.Fatalf("state wrong: %+v", got)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: state survives and still no rollback.
	j2, rb, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rb != nil || !j2.IsCommitted("p/video/0") {
		t.Fatalf("reopen state lost: rb=%+v", rb)
	}
	j2.Close()
}

func TestRollbackAfterInterruptedWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	j, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r := Record{Key: key(i), StartNS: int64(i) * 1e9, EndNS: int64(i+1) * 1e9,
			Source: "v.m4s", Digest: "d"}
		if _, err := j.Commit(r); err != nil {
			t.Fatal(err)
		}
	}
	// Crash while writing a fourth commit.
	partial := Record{Key: key(3), StartNS: 3e9, EndNS: 4e9, Source: "v.m4s", Digest: "d"}
	if err := j.SimulateInterruptedWrite(partial); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// Recovery must roll back and keep only the three confirmed commits.
	j2, rb, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rb == nil {
		t.Fatal("expected rollback report")
	}
	if rb.DiscardedWAL != 1 || rb.FromKey != key(2) {
		t.Fatalf("rollback scope wrong: %+v", rb)
	}
	snap := j2.Snapshot()
	if len(snap.Committed) != 3 {
		t.Fatalf("committed after recovery = %d", len(snap.Committed))
	}
	if j2.IsCommitted(key(3)) {
		t.Fatal("interrupted interval must not be marked committed")
	}
	// Rerun the interrupted interval: it commits exactly once.
	ok, err := j2.Commit(partial)
	if err != nil || !ok {
		t.Fatalf("rerun commit failed: ok=%v err=%v", ok, err)
	}
	ok, _ = j2.Commit(partial)
	if ok {
		t.Fatal("second rerun duplicated the interval")
	}
	j2.Close()
}

func TestRejectsEmptyKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	j, _, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Commit(Record{}); err == nil {
		t.Fatal("empty key accepted")
	}
	j.Close()
}

func key(i int) string {
	return "p/video/" + string(rune('0'+i))
}
