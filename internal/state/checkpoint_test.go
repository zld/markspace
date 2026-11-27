package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPhaseTransitions(t *testing.T) {
	cases := []struct {
		from, to Phase
		ok       bool
	}{
		{PhasePending, PhaseRunning, true},
		{PhaseRunning, PhaseCompleted, true},
		{PhaseRunning, PhaseFailed, true},
		{PhaseFailed, PhaseRunning, true},
		{PhaseCompleted, PhaseRolledBack, true},
		{PhasePending, PhaseCompleted, false},
		{PhaseCompleted, PhaseRunning, false},
		{PhaseRolledBack, PhaseCompleted, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.ok {
			t.Fatalf("%s -> %s = %v want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestSaveLoadRoundTripIsAtomic(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	doc := &CheckpointFile{RunID: "run-1"}
	doc.Put(StageCheckpoint{
		Name:             "ingest",
		Phase:            PhaseCompleted,
		InputHash:        HashStrings("input"),
		OutputHash:       HashStrings("output"),
		RecoveryPosition: 128,
		RecoveryUnit:     "packets",
		Outputs:          []string{"ingest/audio.json"},
	})
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No temp files may remain after a successful save.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "checkpoint.json" {
			t.Fatalf("stray file after atomic save: %s", e.Name())
		}
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cp, ok := loaded.Stage("ingest")
	if !ok || cp.RecoveryPosition != 128 || cp.RecoveryUnit != "packets" {
		t.Fatalf("checkpoint=%+v", cp)
	}
	if cp.InputHash != HashStrings("input") {
		t.Fatal("input hash not preserved")
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "absent"))
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(doc.Stages) != 0 {
		t.Fatalf("stages=%v", doc.Stages)
	}
}

func TestLoadRejectsCorruptAndWrongSchema(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	if err := os.WriteFile(store.Path(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected parse error")
	}
	if err := os.WriteFile(store.Path(), []byte(`{"schema_version": 99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected schema error")
	}
}

func TestFirstIncomplete(t *testing.T) {
	doc := &CheckpointFile{}
	doc.Put(StageCheckpoint{Name: "ingest", Phase: PhaseCompleted, RecoveryPosition: 10})
	doc.Put(StageCheckpoint{Name: "map", Phase: PhaseCompleted, RecoveryPosition: 4})
	plan := []string{"ingest", "map", "merge", "manifest"}
	name, pos, ok := doc.FirstIncomplete(plan)
	if !ok || name != "merge" || pos != 0 {
		t.Fatalf("got %s/%d/%v", name, pos, ok)
	}
	doc.Put(StageCheckpoint{Name: "merge", Phase: PhaseCompleted})
	doc.Put(StageCheckpoint{Name: "manifest", Phase: PhaseCompleted})
	if name, _, ok := doc.FirstIncomplete(plan); ok {
		t.Fatalf("all complete but got %s", name)
	}
}

func TestRollbackStagesInvalidatesDownstream(t *testing.T) {
	doc := &CheckpointFile{}
	plan := []string{"ingest", "map", "merge", "manifest"}
	for _, name := range plan {
		doc.Put(StageCheckpoint{Name: name, Phase: PhaseCompleted, OutputHash: "h-" + name, RecoveryPosition: 3})
	}
	records := doc.RollbackStages(plan, map[string]string{"map": "asset content changed"})
	if len(records) != 3 {
		t.Fatalf("rolled back %d stages, want 3", len(records))
	}
	for _, name := range []string{"ingest"} {
		cp, _ := doc.Stage(name)
		if cp.Phase != PhaseCompleted {
			t.Fatalf("%s should be untouched: %s", name, cp.Phase)
		}
	}
	for _, name := range []string{"map", "merge", "manifest"} {
		cp, _ := doc.Stage(name)
		if cp.Phase != PhaseRolledBack || cp.OutputHash != "" {
			t.Fatalf("%s rollback state=%+v", name, cp)
		}
	}
	cp, _ := doc.Stage("map")
	if cp.RollbackReason != "asset content changed" {
		t.Fatalf("reason=%q", cp.RollbackReason)
	}
	cp, _ = doc.Stage("merge")
	if cp.RollbackReason != "downstream of changed input" {
		t.Fatalf("downstream reason=%q", cp.RollbackReason)
	}
}

func TestRollbackSkipsPendingAndUnknown(t *testing.T) {
	doc := &CheckpointFile{}
	plan := []string{"a", "b"}
	doc.Put(StageCheckpoint{Name: "a", Phase: PhaseCompleted})
	records := doc.RollbackStages(plan, map[string]string{"a": "x"})
	if len(records) != 1 || records[0].Name != "a" {
		t.Fatalf("records=%+v", records)
	}
}

func TestHashStringsIsUnambiguous(t *testing.T) {
	if HashStrings("ab", "c") == HashStrings("a", "bc") {
		t.Fatal("length-prefixed hashing must distinguish boundaries")
	}
	if HashStrings("x") != HashStrings("x") {
		t.Fatal("hashing not deterministic")
	}
}
