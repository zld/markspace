package txn

import (
	"os"
	"path/filepath"
	"testing"

	"medkernel/media"
)

func TestJournalFrameRoundTripAndTornTail(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := j.Append(RecCheck, "audio", map[string]float64{"lufs": -23}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := Replay(dir)
	if err != nil || len(recs) != 3 || recs[2].Seq != 3 {
		t.Fatalf("replay=%d err=%v", len(recs), err)
	}
	// Append a torn frame: valid magic/header start then cut mid-payload.
	path := filepath.Join(dir, JournalName)
	good, _ := os.ReadFile(path)
	torn := append(append([]byte{}, good...), []byte{0x57, 0x4A, 0x01, 0x00, 0x00}...)
	if err := os.WriteFile(path, torn, 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err = Replay(dir)
	if err != nil || len(recs) != 3 {
		t.Fatalf("torn tail must be ignored, got %d records err=%v", len(recs), err)
	}
}

func okStage(n int) StageFunc {
	return func(workdir string) (string, map[string]float64, error) {
		if err := os.WriteFile(filepath.Join(workdir, "out.bin"),
			make([]byte, n), 0o644); err != nil {
			return "", nil, err
		}
		return "hash-" + string(rune('A'+n)), map[string]float64{"v": float64(n)}, nil
	}
}

func TestCommitAndRollback(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCommitter(dir)
	if err != nil {
		t.Fatal(err)
	}
	s1, reused, err := c.Execute(Stage{Name: "ts", InputHash: "in-1", Run: okStage(1)})
	if err != nil || reused || !s1.OK {
		t.Fatalf("first stage: %+v reused=%v err=%v", s1, reused, err)
	}
	failing := Stage{
		Name: "audio", InputHash: "in-2", Run: okStage(2),
		Check: func(m map[string]float64) error {
			return media.New(media.ClassValidation, "test", "metric out of bound")
		},
	}
	if _, _, err := c.Execute(failing); err == nil {
		t.Fatal("failing check must return an error")
	}
	if c.RollbackCount() != 1 {
		t.Fatalf("rollbacks=%d want 1", c.RollbackCount())
	}
	if _, err := os.Stat(filepath.Join(dir, "audio")); !os.IsNotExist(err) {
		t.Fatal("rolled-back stage artifacts must be removed")
	}
	// The journal is retained for diagnosis.
	if recs, _ := Replay(dir); len(recs) < 3 {
		t.Fatalf("diagnostic journal must keep BEGIN/CHECK/ROLLBACK, got %d", len(recs))
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCrashRecoveryMatchesFullRerun(t *testing.T) {
	dir := t.TempDir()

	// Full, uninterrupted reference run.
	refDir := filepath.Join(dir, "ref")
	ref := runThree(t, refDir, false)

	// Interrupted run: stage 2 is killed after its BEGIN/CHECK but before
	// COMMIT, leaving a dangling (non-rolled-back) stage in the journal.
	crashDir := filepath.Join(dir, "crash")
	c1, err := NewCommitter(crashDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c1.Execute(Stage{Name: "ts", InputHash: "in-1", Run: okStage(1)}); err != nil {
		t.Fatal(err)
	}
	if err := c1.journal.Append(RecBegin, "audio", map[string]string{"input_hash": "in-2"}); err != nil {
		t.Fatal(err)
	}
	if err := c1.journal.Append(RecCheck, "audio", map[string]float64{"v": 2}); err != nil {
		t.Fatal(err)
	}
	if err := c1.Close(); err != nil {
		t.Fatal(err)
	}
	// Partial stage artifacts on disk must not survive recovery.
	if err := os.MkdirAll(filepath.Join(crashDir, "audio"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crashDir, "audio", "partial.bin"),
		[]byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Recovery reopens, reuses the committed ts stage and finishes the rest.
	c2 := runThree(t, crashDir, true)
	if c2.CommitHash() != ref.CommitHash() {
		t.Fatalf("recovered hash=%s != full rerun hash=%s",
			c2.CommitHash(), ref.CommitHash())
	}
	if c2.RollbackCount() == 0 {
		t.Fatal("dangling pre-commit stage must be counted as a rollback")
	}
	if got := c2.Committed(); len(got) != 3 {
		t.Fatalf("recovered committed stages=%d want 3", len(got))
	}
}

func runThree(t *testing.T, dir string, expectReuse bool) *Committer {
	t.Helper()
	c, err := NewCommitter(dir)
	if err != nil {
		t.Fatal(err)
	}
	stages := []Stage{
		{Name: "ts", InputHash: "in-1", Run: okStage(1)},
		{Name: "audio", InputHash: "in-2", Run: okStage(2)},
		{Name: "rtp", InputHash: "in-3", Run: okStage(3)},
	}
	for _, st := range stages {
		_, reused, err := c.Execute(st)
		if err != nil {
			t.Fatalf("stage %s: %v", st.Name, err)
		}
		if expectReuse && st.Name == "ts" && !reused {
			t.Fatal("committed stage must be reused on recovery")
		}
	}
	return c
}
