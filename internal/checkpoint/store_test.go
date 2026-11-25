package checkpoint

import (
	"path/filepath"
	"testing"

	errs "mediad/internal/errors"
)

func TestSaveLoadRoundTripAndAtomicity(t *testing.T) {
	dir := t.TempDir()
	st := &State{FixtureID: "fx", InputHash: "in", IndexHash: "ix"}
	st.MarkDone(0)
	st.MarkDone(2)
	st.MarkDone(1)
	store := NewStore(filepath.Join(dir, "cp.json"))
	if store.Exists() {
		t.Fatal("checkpoint must not exist before first save")
	}
	if err := store.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !store.Exists() {
		t.Fatal("checkpoint missing after save")
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LastDoneAU != 3 || len(got.CompletedAU) != 3 ||
		got.InputHash != "in" || got.IndexHash != "ix" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestLoadMissingReturnsNil(t *testing.T) {
	st, err := NewStore(filepath.Join(t.TempDir(), "nope.json")).Load()
	if err != nil || st != nil {
		t.Fatalf("missing checkpoint = (%v, %v)", st, err)
	}
}

func TestVerifyVersionBinding(t *testing.T) {
	st := &State{InputHash: "in-v1", IndexHash: "ix-v1"}
	if err := Verify(st, "in-v1", "ix-v1"); err != nil {
		t.Fatalf("matching versions rejected: %v", err)
	}
	err := Verify(st, "in-v2", "ix-v1")
	if err == nil || !errs.Is(err, errs.CodeCheckpointMismatch) {
		t.Fatalf("changed input must be refused: %v", err)
	}
	err = Verify(st, "in-v1", "ix-v2")
	if err == nil || !errs.Is(err, errs.CodeCheckpointMismatch) {
		t.Fatalf("changed index must be refused: %v", err)
	}
	if err := Verify(nil, "anything", "whatever"); err != nil {
		t.Fatalf("fresh start must always pass: %v", err)
	}
}
