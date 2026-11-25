package checkpoint

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadAndVersionBump(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "state", "checkpoint.json"))
	cp := &Checkpoint{
		Stage:              StageScan,
		InputDigests:       map[string]string{"rep0/frag-001.m4s": "aaa"},
		CompletedFragments: []string{"rep0/frag-001.m4s"},
	}
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 1 || loaded.Stage != StageScan {
		t.Fatalf("unexpected initial checkpoint: %+v", loaded)
	}

	next := &Checkpoint{
		Stage:              StageAlign,
		InputDigests:       map[string]string{"rep0/frag-001.m4s": "aaa"},
		CompletedFragments: []string{"rep0/frag-001.m4s"},
	}
	if err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	loaded, _ = store.Load()
	if loaded.Version != 2 {
		t.Fatalf("version should bump to 2, got %d", loaded.Version)
	}
}

func TestRefusesStageRollback(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "cp.json"))
	advanced := &Checkpoint{Stage: StageManifest, InputDigests: map[string]string{}}
	if err := store.Save(advanced); err != nil {
		t.Fatal(err)
	}
	rollback := &Checkpoint{Stage: StageScan, InputDigests: map[string]string{}}
	if err := store.Save(rollback); err == nil {
		t.Fatal("stage rollback must be rejected to protect the safe checkpoint")
	}
}

func TestVerifyInputAndCompletion(t *testing.T) {
	cp := &Checkpoint{
		Stage:              StageAlign,
		InputDigests:       map[string]string{"rep0/frag-001.m4s": "good"},
		CompletedFragments: []string{"rep0/frag-001.m4s"},
	}
	if !cp.VerifyInput("rep0/frag-001.m4s", "good") {
		t.Fatal("matching digest must verify")
	}
	if cp.VerifyInput("rep0/frag-001.m4s", "tampered") {
		t.Fatal("changed input must fail verification")
	}
	if cp.VerifyInput("rep0/missing.m4s", "x") {
		t.Fatal("unknown input must not verify")
	}
	if !cp.IsFragmentCompleted("rep0/frag-001.m4s") {
		t.Fatal("completed fragment lookup failed")
	}
	if cp.IsFragmentCompleted("rep0/frag-002.m4s") {
		t.Fatal("unseen fragment cannot be completed")
	}
	if !cp.Reached(StageScan) || cp.Reached(StageComplete) {
		t.Fatal("stage reachability wrong")
	}
}

func TestRejectsUnknownStage(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "cp.json"))
	if err := store.Save(&Checkpoint{Stage: "bogus", InputDigests: map[string]string{}}); err == nil {
		t.Fatal("unknown stage must be rejected")
	}
}
