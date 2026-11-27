package job

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"regress/internal/model"
)

func TestStageProgressionAndCheckpoint(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j, rec, err := store.Start("task-1", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Recovered || j.State.Stage != model.StageReceive {
		t.Fatalf("fresh job wrong: %+v stage=%s", rec, j.State.Stage)
	}
	for _, st := range []model.Stage{model.StageParse, model.StageArtifact, model.StageCommit} {
		if err := j.Advance(st); err != nil {
			t.Fatalf("advance %s: %v", st, err)
		}
	}
	if _, err := os.Stat(filepath.Join(j.Dir(), checkpointFile)); err != nil {
		t.Fatalf("checkpoint not on disk: %v", err)
	}
	// Reopen: recovery must be flagged and the committed stage retained.
	j2, rec2, err := store.Start("task-1", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if !rec2.Recovered || j2.State.Stage != model.StageCommit ||
		j2.State.Attempts != 2 {
		t.Fatalf("recovery wrong: rec=%+v state=%+v", rec2, j2.State)
	}
}

func TestPostArtifactCrashRecoversWithoutRepublish(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir)

	j, _, err := store.Start("task-crash", "h")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Advance(model.StageParse); err != nil {
		t.Fatal(err)
	}
	rec1, err := j.WriteArtifact("ev-1", "ev-1.bin", []byte("payload-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !rec1.Written || j.State.Stage != model.StageArtifact {
		t.Fatalf("artifact stage wrong: %+v", rec1)
	}

	// Simulate a crash between artifact write and commit: reopen the store
	// and re-run with the same inputs.
	store2, _ := OpenStore(dir)
	j2, rec, err := store2.Start("task-crash", "h")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Recovered || rec.VerifiedArtifacts != 1 {
		t.Fatalf("recovery = %+v, want 1 verified artifact", rec)
	}
	// Re-writing the same key must not touch the file or add a record.
	rec2, err := j2.WriteArtifact("ev-1", "ev-1.bin", []byte("payload-1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec2.Sha256 != rec1.Sha256 || len(j2.State.Artifacts) != 1 {
		t.Fatalf("artifact republished: %+v (%d records)", rec2,
			len(j2.State.Artifacts))
	}
	// Now the missing commit is made up.
	if err := j2.CommitKey("ev-1"); err != nil {
		t.Fatal(err)
	}
	if err := j2.CommitKey("ev-1"); err != nil {
		t.Fatal(err)
	}
	if len(j2.State.Published) != 1 {
		t.Fatalf("published = %v, want exactly one entry", j2.State.Published)
	}
}

func TestTamperedArtifactFailsRecovery(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir)
	j, _, _ := store.Start("task-tamper", "h")
	rec, err := j.WriteArtifact("ev", "ev.bin", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the artifact bytes behind the store's back.
	path := filepath.Join(j.Dir(), rec.RelPath)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	store2, _ := OpenStore(dir)
	_, _, err = store2.Start("task-tamper", "h")
	if !errors.Is(err, model.ErrRecovery) {
		t.Fatalf("want ErrRecovery, got %v", err)
	}
	me := model.AsError(err)
	if me == nil || me.Stage != model.StageArtifact {
		t.Fatalf("recovery error not locatable: %v", err)
	}
}

func TestInputHashMismatchRejected(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir)
	j, _, _ := store.Start("task", "hash-a")
	if err := j.Advance(model.StageCommit); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.Start("task", "hash-b")
	if !errors.Is(err, model.ErrRecovery) {
		t.Fatalf("want ErrRecovery on input drift, got %v", err)
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	j, _, _ := store.Start("task", "h")
	for _, name := range []string{"../escape.bin", "/abs/path.bin", "a/../../b.bin"} {
		if _, err := j.WriteArtifact("k", name, []byte("x")); err == nil {
			t.Fatalf("escaping path accepted: %q", name)
		}
	}
}

// TestReachIsForwardOnlyIdempotent pins the recovery contract: re-executing
// an earlier stage's checkpoint on a resumed job must not regress the stage,
// while a genuinely forward move is still recorded.
func TestReachIsForwardOnlyIdempotent(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	j, _, _ := store.Start("task", "h")
	if err := j.Reach(model.StageArtifact); err != nil {
		t.Fatal(err)
	}
	if j.State.Stage != model.StageArtifact {
		t.Fatalf("stage = %s, want artifact", j.State.Stage)
	}
	rev := j.State.Revision
	// Reaching parse (earlier) is a no-op, not a regression.
	if err := j.Reach(model.StageParse); err != nil {
		t.Fatalf("reach backwards: %v", err)
	}
	if j.State.Stage != model.StageArtifact || j.State.Revision != rev {
		t.Fatalf("backwards reach mutated state: stage=%s rev=%d",
			j.State.Stage, j.State.Revision)
	}
	// Reach to the same stage is also a no-op.
	if err := j.Reach(model.StageArtifact); err != nil {
		t.Fatal(err)
	}
	if j.State.Revision != rev {
		t.Fatal("same-stage reach wrote a redundant checkpoint")
	}
	// A forward reach still works and persists across a reopen.
	if err := j.Reach(model.StageCommit); err != nil {
		t.Fatal(err)
	}
	store2, _ := OpenStore(store.root)
	j2, rec, err := store2.Start("task", "h")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Recovered || j2.State.Stage != model.StageCommit {
		t.Fatalf("forward reach not durable: %+v", j2.State)
	}
}

func TestFailDoesNotReportCompletion(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	j, _, _ := store.Start("task", "h")
	_ = j.Advance(model.StageParse)
	srcErr := model.NewError(model.StageParse, "boom", "f.mp4", "track", 42,
		errors.New("kaboom"))
	if err := j.Fail(srcErr); err != nil {
		t.Fatal(err)
	}
	if j.State.LastError == nil || j.State.LastError.Offset != 42 ||
		j.State.Stage == model.StageCommit {
		t.Fatalf("failure state wrong: %+v stage=%s", j.State.LastError,
			j.State.Stage)
	}
	// Reopening exposes the recorded failure rather than a clean commit.
	j2, _, err := store.Start("task", "h")
	if err != nil {
		t.Fatal(err)
	}
	if j2.State.LastError == nil {
		t.Fatal("recorded error lost across restart")
	}
}
