package txn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestJournalAppendAndReplay(t *testing.T) {
	base := t.TempDir()
	j, err := OpenJournal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Log(0, "sync-align", HashBytes([]byte("in0")), "00-sync-align"); err != nil {
		t.Fatal(err)
	}
	if err := j.Prepare(0, HashBytes([]byte("out0"))); err != nil {
		t.Fatal(err)
	}
	if err := j.Commit(0); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen and replay.
	j2, err := OpenJournal(base)
	if err != nil {
		t.Fatal(err)
	}
	defer j2.Close()
	if j2.LastCommitted() != 0 {
		t.Fatalf("last committed = %d", j2.LastCommitted())
	}
	es := j2.Entries()
	if len(es) != 3 || es[2].State != StateCommitted {
		t.Fatalf("replayed entries = %+v", es)
	}
	// Absolute paths and traversal must be rejected.
	if _, err := j2.Log(1, "x", "h", filepath.Join(t.TempDir(), "abs")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("absolute path: %v", err)
	}
	if _, err := j2.Log(1, "x", "h", "../escape"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("traversal path: %v", err)
	}
}

func TestAtomicCommitAndRollbackBoundary(t *testing.T) {
	base := t.TempDir()
	c, err := NewCoordinator(base)
	if err != nil {
		t.Fatal(err)
	}
	stages := []Stage{
		{Name: "sync-align", InputHash: HashBytes([]byte("in0")), Run: func(d string) ([]string, error) {
			return []string{writeFile(t, d, "a.bin", "A")}, nil
		}},
		{Name: "audio-normalize", InputHash: HashBytes([]byte("in1")), Run: func(d string) ([]string, error) {
			return []string{writeFile(t, d, "b.bin", "B")}, nil
		}},
		{Name: "color-normalize", InputHash: HashBytes([]byte("in2")), Run: func(d string) ([]string, error) {
			return nil, errors.New("boom")
		}},
	}
	rp, err := c.RunPlan(stages)
	if err == nil {
		t.Fatal("failing stage must return error")
	}
	if !errors.Is(err, ErrStageFailed) {
		t.Fatalf("want ErrStageFailed, got %v", err)
	}
	if rp.Boundary != "audio-normalize" {
		t.Fatalf("rollback boundary = %q, want audio-normalize", rp.Boundary)
	}
	// First two outputs published, third cleaned up.
	if _, err := os.Stat(filepath.Join(base, "out", "01-audio-normalize", "b.bin")); err != nil {
		t.Fatalf("committed stage missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "out", "02-color-normalize")); !os.IsNotExist(err) {
		t.Fatalf("failed stage artifacts must be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "tmp", "02-color-normalize")); !os.IsNotExist(err) {
		t.Fatal("temp dir of failed stage must be removed")
	}
}

func TestCrashRecoverySkipsVerifiedStages(t *testing.T) {
	base := t.TempDir()
	plan := []Stage{
		{Name: "sync-align", InputHash: HashBytes([]byte("in0")), Run: func(d string) ([]string, error) {
			return []string{writeFile(t, d, "a.bin", "A")}, nil
		}},
		{Name: "audio-normalize", InputHash: HashBytes([]byte("in1")), Run: func(d string) ([]string, error) {
			return []string{writeFile(t, d, "b.bin", "B")}, nil
		}},
	}
	// First run completes fully.
	c1, err := NewCoordinator(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c1.RunPlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := c1.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: reopen a new coordinator over the same journal.
	start := time.Now()
	c2, err := NewCoordinator(base)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	rp, err := c2.Recover(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range plan {
		if rp.Stages[st.Name] != ReplaySkip {
			t.Fatalf("stage %s decision = %v, want skip", st.Name, rp.Stages[st.Name])
		}
	}
	if rp.Boundary != "audio-normalize" || len(rp.Skipped) != 2 {
		t.Fatalf("recovery plan = %+v", rp)
	}
	elapsed := time.Since(start)
	t.Logf("recovery_elapsed_ms=%d", elapsed.Milliseconds())
}

func TestRecoveryInputHashMismatch(t *testing.T) {
	base := t.TempDir()
	plan := []Stage{{Name: "sync-align", InputHash: HashBytes([]byte("orig")), Run: func(d string) ([]string, error) {
		return []string{writeFile(t, d, "a.bin", "A")}, nil
	}}}
	c, _ := NewCoordinator(base)
	if _, err := c.RunPlan(plan); err != nil {
		t.Fatal(err)
	}
	// Same stage name, changed input hash.
	changed := []Stage{{Name: "sync-align", InputHash: HashBytes([]byte("changed")), Run: plan[0].Run}}
	if _, err := c.Recover(changed); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("changed input must fail recovery: %v", err)
	}
}

func TestPreparedButNotCommittedRollsBack(t *testing.T) {
	base := t.TempDir()
	c, _ := NewCoordinator(base)
	j := c.Journal()
	_, _ = j.Log(0, "sync-align", HashBytes([]byte("in")), "00-sync-align")
	_ = os.MkdirAll(filepath.Join(base, "tmp", "00-sync-align"), 0o755)
	_ = os.WriteFile(filepath.Join(base, "tmp", "00-sync-align", "x.bin"), []byte("x"), 0o644)
	_ = j.Prepare(0, HashBytes([]byte("x")))
	// No commit: process dies. Reopen and recover.
	_ = c.Close()
	c2, _ := NewCoordinator(base)
	defer c2.Close()
	plan := []Stage{{Name: "sync-align", InputHash: HashBytes([]byte("in")), Run: func(d string) ([]string, error) {
		return []string{writeFile(t, d, "a.bin", "A")}, nil
	}}}
	rp, err := c2.Recover(plan)
	if err != nil {
		t.Fatal(err)
	}
	if rp.Stages["sync-align"] != ReplayRollback {
		t.Fatalf("decision = %v, want rollback", rp.Stages["sync-align"])
	}
	if _, err := os.Stat(filepath.Join(base, "tmp", "00-sync-align")); !os.IsNotExist(err) {
		t.Fatal("partial temp artifacts must be cleaned on recovery")
	}
}

func TestDeterministicArtifactsAndJournal(t *testing.T) {
	stage := Stage{Name: "sync-align", InputHash: HashBytes([]byte("in")), Run: func(d string) ([]string, error) {
		return []string{writeFile(t, d, "a.bin", "fixed-bytes")}, nil
	}}
	run := func() (string, string) {
		base := t.TempDir()
		c, _ := NewCoordinator(base)
		if _, err := c.RunPlan([]Stage{stage}); err != nil {
			t.Fatal(err)
		}
		out, _ := hashDir(filepath.Join(base, "out", "00-sync-align"))
		journal, _ := os.ReadFile(filepath.Join(base, "journal.wal"))
		return out, string(journal)
	}
	h1, j1 := run()
	h2, j2 := run()
	if h1 != h2 || j1 != j2 {
		t.Fatalf("non-deterministic output hash=%s/%s journal_equal=%v", h1, h2, j1 == j2)
	}
	if strings.Contains(j1, t.TempDir()) || strings.Contains(j1, "/Users/") {
		t.Fatal("journal must not contain absolute system paths")
	}
}
