package task

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeFromLastCheckpoint(t *testing.T) {
	dir := t.TempDir()
	if _, err := SaveCheckpoint(dir, "k", StageParse, map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveCheckpoint(dir, "k", StageMetadata, map[string]string{"b": "2"}); err != nil {
		t.Fatal(err)
	}
	plan, err := LoadResumePlan(dir, "k")
	if err != nil {
		t.Fatal(err)
	}
	if plan.CompletedStage != StageMetadata || plan.ResumeStage != StageValidate {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestCorruptCheckpointFallsBackToPreviousStage(t *testing.T) {
	dir := t.TempDir()
	if _, err := SaveCheckpoint(dir, "k", StageParse, map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveCheckpoint(dir, "k", StageMetadata, map[string]string{"b": "2"}); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(dir, "k.metadata.ckpt")
	if err := os.WriteFile(metaPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := LoadResumePlan(dir, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.CorruptStages) != 1 || plan.CorruptStages[0] != StageMetadata {
		t.Fatalf("corrupt stages = %+v", plan.CorruptStages)
	}
	if plan.CompletedStage != StageParse || plan.ResumeStage != StageMetadata {
		t.Fatalf("expected fallback to parse checkpoint and redo metadata, got %+v", plan)
	}
	if _, err := os.Stat(metaPath + ".corrupt"); err != nil {
		t.Fatalf("corrupt checkpoint was not quarantined: %v", err)
	}
}

func TestStatePersistence(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkRunning("k", []string{"/tmp/a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteStage("k", StageParse); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := st.Load("k")
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if loaded.CompletedStage != StageParse || loaded.Attempt != 1 {
		t.Fatalf("unexpected state: %+v", loaded)
	}
}
