package checkpoint

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCheckpointAtomicSaveLoad(t *testing.T) {
	j, err := Open(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := j.Load(context.Background()); err != nil || found {
		t.Fatalf("empty journal found=%v err=%v", found, err)
	}
	cp := Checkpoint{InputDigest: "abc", Stage: StageCached, Cursor: 9}
	if err := j.Save(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	got, found, err := j.Load(context.Background())
	if err != nil || !found || got != cp {
		t.Fatalf("load=%+v found=%v err=%v", got, found, err)
	}
}

func TestStageJournalAppendReplay(t *testing.T) {
	j, _ := Open(t.TempDir())
	for _, r := range []StageRecord{{Cursor: 1, Stage: StageCached}, {Cursor: 2, Stage: StageCheckpoint}} {
		if err := j.AppendStage(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := j.ReadStages(context.Background())
	if err != nil || len(recs) != 2 || recs[1].Stage != StageCheckpoint {
		t.Fatalf("records=%+v err=%v", recs, err)
	}
}

func TestSaveRejectsIncompleteCheckpoint(t *testing.T) {
	j, _ := Open(t.TempDir())
	if err := j.Save(context.Background(), Checkpoint{Stage: StageCached}); err == nil {
		t.Fatal("expected invalid checkpoint error")
	}
}
