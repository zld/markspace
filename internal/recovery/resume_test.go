package recovery_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/model"
	"mediapack/internal/recovery"
)

func TestStageTransitionsAndIllegalJump(t *testing.T) {
	cp := recovery.New(17, "digest")
	if cp.Stage != model.StageCreated {
		t.Fatalf("initial stage = %s", cp.Stage)
	}
	for _, next := range []model.Stage{
		model.StageParsed, model.StageComposed, model.StageExported, model.StageVerified,
	} {
		if err := cp.Transition(next); err != nil {
			t.Fatalf("legal transition to %s failed: %v", next, err)
		}
	}
	if err := cp.Transition(model.StageParsed); err == nil {
		t.Fatal("backwards jump must fail")
	}
}

func TestLayerCheckpointAnchors(t *testing.T) {
	cp := recovery.New(17, "d")
	cp.EnsureLayer("low")
	cp.EnsureLayer("low") // idempotent
	cp.EnsureLayer("high")
	if len(cp.Layers) != 2 {
		t.Fatalf("layers = %d, want 2", len(cp.Layers))
	}
	if err := cp.MarkSegmentCompleted("low", 0, 3, 11); err != nil {
		t.Fatal(err)
	}
	if err := cp.MarkSegmentCompleted("low", 1, 7, 23); err != nil {
		t.Fatal(err)
	}
	lc, ok := cp.Layer("low")
	if !ok || lc.LastSegment != 1 || lc.LastFrame != 7 || lc.LastSeq != 23 {
		t.Fatalf("anchor wrong: %+v", lc)
	}
	// Skipping a segment must be rejected.
	if err := cp.MarkSegmentCompleted("low", 3, 9, 30); err == nil {
		t.Fatal("segment skip must fail")
	}
	// Each layer keeps an independent anchor.
	hc, _ := cp.Layer("high")
	if hc.LastSegment != -1 {
		t.Fatal("high layer anchor must stay independent")
	}
}

func TestCheckpointSaveLoadResume(t *testing.T) {
	root := t.TempDir()
	cp := recovery.New(17, "digest")
	cp.EnsureLayer("low")
	if err := cp.Transition(model.StageParsed); err != nil {
		t.Fatal(err)
	}
	if err := cp.Transition(model.StageComposed); err != nil {
		t.Fatal(err)
	}
	if err := cp.MarkSegmentCompleted("low", 0, 3, 11); err != nil {
		t.Fatal(err)
	}
	if err := cp.Save(root); err != nil {
		t.Fatal(err)
	}
	loaded, err := recovery.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Stage != model.StageComposed || !loaded.MatchesInput(17, "digest") {
		t.Fatalf("loaded checkpoint wrong: %+v", loaded)
	}
	if loaded.MatchesInput(18, "digest") {
		t.Fatal("different seed must not match")
	}
	lc, _ := loaded.Layer("low")
	if lc.LastSegment != 0 || lc.LastFrame != 3 {
		t.Fatalf("resume anchor wrong: %+v", lc)
	}
}

func TestLoadMissingCheckpoint(t *testing.T) {
	root := t.TempDir()
	if _, err := recovery.Load(root); !os.IsNotExist(err) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
	// And a malformed checkpoint yields an invalid-input error.
	p := filepath.Join(root, "checkpoint.json")
	if err := os.WriteFile(p, []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Load(root); !model.IsKind(err, model.KindInvalidInput) {
		t.Fatalf("expected invalid-input, got %v", err)
	}
}
