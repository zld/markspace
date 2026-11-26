package txn

import (
	"os"
	"path/filepath"
	"testing"
)

func stageN(t *testing.T, j *Journal, n int) []Segment {
	t.Helper()
	segs := make([]Segment, 0, n)
	for i := 1; i <= n; i++ {
		path := j.StagePath(i)
		if err := os.WriteFile(path, []byte("segment-"+itoa(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		seg, err := j.StageSegment(i)
		if err != nil {
			t.Fatal(err)
		}
		segs = append(segs, *seg)
	}
	return segs
}

func TestCommitHappyPath(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	segs := stageN(t, j, 4)
	m, err := j.Commit(segs, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 4 || m.CueCount != 8 || m.Digest == "" {
		t.Fatalf("manifest mismatch: %+v", m)
	}
	// Re-commit is idempotent and returns the same manifest.
	m2, err := j.Commit(segs, 8)
	if err != nil || m2.Digest != m.Digest || m2.Version != m.Version {
		t.Fatalf("re-commit must be idempotent: %v %+v", err, m2)
	}
}

func TestStageSegmentIdempotent(t *testing.T) {
	j, _ := Open(t.TempDir())
	if err := os.WriteFile(j.StagePath(1), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s1, err := j.StageSegment(1)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := j.StageSegment(1)
	if err != nil || s1.SHA256 != s2.SHA256 {
		t.Fatalf("re-stage must return the same segment: %v", err)
	}
	recs, _ := j.ReadRecords()
	count := 0
	for _, r := range recs {
		if r.Step == "stage" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("re-stage must not append a second journal record, got %d", count)
	}
}

func TestHashMismatchRollsBackAndKeepsManifest(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	segs := stageN(t, j, 2)
	// Establish a first good manifest.
	good, err := j.Commit(segs, 4)
	if err != nil {
		t.Fatal(err)
	}
	// A second transaction in a fresh run directory would replace it; instead
	// model in-place verification failure by tampering with a staged payload
	// while its journaled hash still records the original digest.
	if err := os.WriteFile(j.StagePath(2), []byte("corrupted-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = j.VerifySegments(segs)
	if err == nil {
		t.Fatal("corrupt segment must fail verification")
	}
	if reason := j.RollbackReason(); reason == "" {
		t.Fatal("rollback reason must be journaled")
	}
	// The committed manifest is unchanged on disk.
	cur, err := j.CurrentManifest()
	if err != nil || cur.Digest != good.Digest || cur.Version != good.Version {
		t.Fatalf("previous manifest must survive rollback: %v %+v", err, cur)
	}
}

func TestResumeCompletesInterruptedRun(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	_ = stageN(t, j, 4)
	// Simulate a crash after staging but before commit.
	if j.IsCommitted() {
		t.Fatal("precondition: not committed")
	}
	jr, _ := Open(dir)
	m, st, err := jr.Resume(8)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if st.Replayed != 1 || st.DuplicateCues != 0 || !st.Committed {
		t.Fatalf("unexpected recovery status: %+v", st)
	}
	if len(m.Segments) != 4 {
		t.Fatalf("resumed manifest missing segments: %+v", m)
	}
	// Resuming again replays nothing.
	_, st2, err := jr.Resume(8)
	if err != nil || st2.Replayed != 0 {
		t.Fatalf("second resume must replay 0 steps: %v %+v", err, st2)
	}
}

func TestResumeHashEquivalentToUninterrupted(t *testing.T) {
	build := func(dir string) (*Manifest, error) {
		j, _ := Open(dir)
		segs := stageN(t, j, 4)
		return j.Commit(segs, 8)
	}
	dirClean := t.TempDir()
	mClean, err := build(dirClean)
	if err != nil {
		t.Fatal(err)
	}
	dirCrash := t.TempDir()
	j, _ := Open(dirCrash)
	_ = stageN(t, j, 4) // crash point: staged, not committed
	jr, _ := Open(dirCrash)
	mResume, _, err := jr.Resume(8)
	if err != nil {
		t.Fatal(err)
	}
	if mResume.Digest != mClean.Digest {
		t.Fatalf("resumed manifest digest %s != uninterrupted %s", mResume.Digest, mClean.Digest)
	}
	for i := range mClean.Segments {
		if mClean.Segments[i].SHA256 != mResume.Segments[i].SHA256 {
			t.Fatal("per-segment hashes must match between interrupted and clean runs")
		}
	}
}

func TestCommitFailurePreservesPreviousManifest(t *testing.T) {
	// A prior run produced the manifest that must survive the next failure.
	dirPrior := t.TempDir()
	jPrior, _ := Open(dirPrior)
	good, err := jPrior.Commit(stageN(t, jPrior, 2), 4)
	if err != nil {
		t.Fatal(err)
	}

	// The new run shares the manifest location but starts from staged state
	// (no commit record), then a segment is tampered with before Commit.
	dirNew := t.TempDir()
	j, _ := Open(dirNew)
	seg := func() Segment {
		if err := os.WriteFile(j.StagePath(1), []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := j.StageSegment(1)
		if err != nil {
			t.Fatal(err)
		}
		return *s
	}()
	// Seed the "previous manifest" that a failed commit must not overwrite.
	if err := os.MkdirAll(filepath.Join(dirNew, "txn"), 0o755); err != nil {
		t.Fatal(err)
	}
	goodBytes, err := os.ReadFile(filepath.Join(dirPrior, "txn", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestDest := filepath.Join(dirNew, "txn", "manifest.json")
	if err := os.WriteFile(manifestDest, goodBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.StagePath(1), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Commit([]Segment{seg}, 1); err == nil {
		t.Fatal("commit with a tampered segment must fail")
	}
	cur, err := j.CurrentManifest()
	if err != nil || cur.Version != good.Version || cur.Digest != good.Digest {
		t.Fatalf("failed commit must leave the prior manifest intact: %v %+v", err, cur)
	}
}

func TestReset(t *testing.T) {
	dir := t.TempDir()
	j, _ := Open(dir)
	stageN(t, j, 1)
	if err := j.Reset(); err != nil {
		t.Fatal(err)
	}
	if recs, _ := j.ReadRecords(); len(recs) != 0 {
		t.Fatal("reset must empty the journal")
	}
	if _, err := os.Stat(filepath.Join(dir, "txn", "staging")); err != nil {
		t.Fatalf("reset must recreate the staging directory: %v", err)
	}
}
