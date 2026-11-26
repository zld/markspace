package index

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/clock"
)

func TestJournalRoundTripAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	j, err := NewJournal(dir, "/input")
	if err != nil {
		t.Fatal(err)
	}
	a := Artifact{StorePath: "v/seg-000001-abc.m4s", SHA256: "abc", Size: 100,
		RepID: "v", SegmentIdx: 1}
	if err := j.RegisterArtifact(a); err != nil {
		t.Fatal(err)
	}
	if err := j.CheckpointChunk(1, 2); err != nil {
		t.Fatal(err)
	}
	if err := j.AdvanceStage(StageWrite); err != nil {
		t.Fatal(err)
	}

	// Reopen: state must survive.
	j2, err := NewJournal(dir, "/input")
	if err != nil {
		t.Fatal(err)
	}
	st := j2.State()
	if st.NextChunk != 1 || st.SegmentsDone != 1 || !st.StageCompleted[StageWrite] {
		t.Fatalf("reloaded state = %+v", st)
	}
	if got := st.Artifacts[a.StorePath]; got == nil || got.SHA256 != "abc" {
		t.Fatalf("artifact lost: %+v", got)
	}
}

func TestIdempotentReregisterNoDuplicate(t *testing.T) {
	dir := t.TempDir()
	j, _ := NewJournal(dir, "/input")
	a := Artifact{StorePath: "v/x.m4s", SHA256: "deadbeef", Size: 10}
	if err := j.RegisterArtifact(a); err != nil {
		t.Fatal(err)
	}
	// Same path + same hash: accepted silently, segment count must not grow.
	if err := j.RegisterArtifact(a); err != nil {
		t.Fatalf("idempotent re-register failed: %v", err)
	}
	if j.State().SegmentsDone != 1 {
		t.Fatalf("segments done = %d, want 1", j.State().SegmentsDone)
	}
}

func TestHashConflictFails(t *testing.T) {
	dir := t.TempDir()
	j, _ := NewJournal(dir, "/input")
	if err := j.RegisterArtifact(Artifact{StorePath: "v/x.m4s", SHA256: "aaa"}); err != nil {
		t.Fatal(err)
	}
	err := j.RegisterArtifact(Artifact{StorePath: "v/x.m4s", SHA256: "bbb"})
	if clock.KindOf(err) != clock.KindHashConflict {
		t.Fatalf("want hash conflict, got %v", err)
	}
}

func TestCrashAfterWriteSkipsCommit(t *testing.T) {
	dir := t.TempDir()
	j, _ := NewJournal(dir, "/input")
	j.SetCrashConfig(CrashConfig{CrashAfterWrite: 1})
	err := j.RegisterArtifact(Artifact{StorePath: "v/x.m4s", SHA256: "aaa"})
	if _, ok := err.(*InjectedCrash); !ok {
		t.Fatalf("want injected crash, got %v", err)
	}
	// Journal file must not contain the uncommitted artifact.
	if _, statErr := os.Stat(filepath.Join(dir, "journal.json")); !os.IsNotExist(statErr) {
		j2, _ := NewJournal(dir, "/input")
		if len(j2.State().Artifacts) != 0 {
			t.Fatalf("artifact leaked into journal: %+v", j2.State().Artifacts)
		}
	}
}

func TestStageOrdering(t *testing.T) {
	want := []Stage{StageParse, StageAlign, StageVerify, StageWrite, StagePublish}
	if len(OrderedStages) != len(want) {
		t.Fatalf("stage count = %d", len(OrderedStages))
	}
	for i := range want {
		if OrderedStages[i] != want[i] {
			t.Fatalf("stage %d = %s want %s", i, OrderedStages[i], want[i])
		}
	}
}

func TestMarkPublished(t *testing.T) {
	dir := t.TempDir()
	j, _ := NewJournal(dir, "/input")
	if err := j.MarkPublished("sha123"); err != nil {
		t.Fatal(err)
	}
	st := j.State()
	if !st.Published || st.ManifestSHA != "sha123" || !st.StageCompleted[StagePublish] {
		t.Fatalf("publish state = %+v", st)
	}
}
