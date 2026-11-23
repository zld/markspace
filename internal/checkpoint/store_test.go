package checkpoint_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/checkpoint"
)

func TestSaveIsAtomicAndReloadable(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "sub", "checkpoint.json")}
	st := checkpoint.State{InputDigest: "in", ConfigDigest: "cfg", LastStage: checkpoint.StageTimeline, ResumeAt: 123}
	st = checkpoint.Advance(st, checkpoint.StageIngest, 100)
	st = checkpoint.Advance(st, checkpoint.StageTimeline, 123)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastStage != checkpoint.StageTimeline || loaded.ResumeAt != 123 {
		t.Fatalf("loaded=%+v", loaded)
	}
	// No temp files are left behind.
	entries, err := os.ReadDir(filepath.Dir(store.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "checkpoint.json" {
			t.Fatalf("stray file %q left in checkpoint directory", e.Name())
		}
	}
}

func TestSaveDoesNotClobberOnMarshalFailure(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "checkpoint.json")}
	if err := store.Save(checkpoint.State{LastStage: checkpoint.StageIngest, InputDigest: "v1"}); err != nil {
		t.Fatal(err)
	}
	// Reloading must observe exactly the saved state and nothing partial.
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InputDigest != "v1" {
		t.Fatalf("previous checkpoint lost: %+v", loaded)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &checkpoint.Store{Path: path}
	_, err := store.Load()
	if !errors.Is(err, checkpoint.ErrCorrupt) {
		t.Fatalf("expected corrupt error, got %v", err)
	}
}

func TestLoadRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"last_stage":"ingest"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&checkpoint.Store{Path: path}).Load(); !errors.Is(err, checkpoint.ErrCorrupt) {
		t.Fatalf("expected corrupt error, got %v", err)
	}
}

func TestRecoverRefusesChangedInputs(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "checkpoint.json")}
	if err := store.Save(checkpoint.State{InputDigest: "A", ConfigDigest: "C", LastStage: checkpoint.StageRepair, ResumeAt: 5}); err != nil {
		t.Fatal(err)
	}
	decision, err := checkpoint.Recover(store, checkpoint.Expect{InputDigest: "B", ConfigDigest: "C"})
	if !errors.Is(err, checkpoint.ErrMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
	if decision.Resume {
		t.Fatal("resumed despite changed inputs")
	}
	if decision.Reason == "" {
		t.Fatal("refusal has no reason")
	}
}

func TestRecoverRefusesChangedConfig(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "checkpoint.json")}
	if err := store.Save(checkpoint.State{InputDigest: "A", ConfigDigest: "C1", LastStage: checkpoint.StageRepair}); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Recover(store, checkpoint.Expect{InputDigest: "A", ConfigDigest: "C2"}); !errors.Is(err, checkpoint.ErrMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestRecoverKeepsVerifiedOutputIntact(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "checkpoint.json")}
	if err := store.Save(checkpoint.State{InputDigest: "A", ConfigDigest: "C", LastStage: checkpoint.StageFrames}); err != nil {
		t.Fatal(err)
	}
	verified := filepath.Join(dir, "frames.jsonl")
	if err := os.WriteFile(verified, []byte("verified"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := checkpoint.Recover(store, checkpoint.Expect{InputDigest: "B", ConfigDigest: "C", VerifiedOutput: verified})
	if !errors.Is(err, checkpoint.ErrMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
	got, err := os.ReadFile(verified)
	if err != nil || string(got) != "verified" {
		t.Fatalf("verified output changed: %q %v", got, err)
	}
}

func TestRecoverAcceptsMatchingCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "checkpoint.json")}
	st := checkpoint.Advance(checkpoint.State{InputDigest: "A", ConfigDigest: "C"}, checkpoint.StageIngest, 10)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	d, err := checkpoint.Recover(store, checkpoint.Expect{InputDigest: "A", ConfigDigest: "C"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Resume || d.LastStage != checkpoint.StageIngest {
		t.Fatalf("decision=%+v", d)
	}
	next, ok := checkpoint.NextStage(d.LastStage)
	if !ok || next != checkpoint.StageTimeline {
		t.Fatalf("next stage=%v ok=%v", next, ok)
	}
}

func TestRecoverWithoutCheckpointStartsFresh(t *testing.T) {
	store := &checkpoint.Store{Path: filepath.Join(t.TempDir(), "missing.json")}
	d, err := checkpoint.Recover(store, checkpoint.Expect{InputDigest: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Resume {
		t.Fatal("resumed without a checkpoint")
	}
}

func TestDigestInputsIsOrderAndLengthSensitive(t *testing.T) {
	a := checkpoint.DigestInputs([]byte("ab"), []byte("c"))
	b := checkpoint.DigestInputs([]byte("a"), []byte("bc"))
	if a == b {
		t.Fatal("input digest does not distinguish boundaries")
	}
	if checkpoint.DigestInputs([]byte("ab"), []byte("c")) != a {
		t.Fatal("input digest is not deterministic")
	}
}
