package checkpoint_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mediacore/internal/checkpoint"
)

func TestInterruptedRunResumesFromLastCompleteStage(t *testing.T) {
	dir := t.TempDir()
	store := &checkpoint.Store{Path: filepath.Join(dir, "cp.json")}
	st := checkpoint.State{InputDigest: "in", ConfigDigest: "cfg"}
	st = checkpoint.Advance(st, checkpoint.StageIngest, 100)
	st = checkpoint.Advance(st, checkpoint.StageTimeline, 100)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: no further stage is written. Recovery must resume at
	// timeline, not claim later stages completed.
	d, err := checkpoint.Recover(store, checkpoint.Expect{InputDigest: "in", ConfigDigest: "cfg"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Resume || d.LastStage != checkpoint.StageTimeline {
		t.Fatalf("decision=%+v", d)
	}
	next, ok := checkpoint.NextStage(d.LastStage)
	if !ok || next != checkpoint.StageRepair {
		t.Fatalf("next=%v ok=%v", next, ok)
	}
	// An atomic save leaves no temp artifacts behind even when overwritten.
	for i := 0; i < 5; i++ {
		if err := store.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("found %d files in checkpoint dir", len(entries))
	}
	if info, _ := entries[0].Info(); time.Since(info.ModTime()) > time.Minute {
		t.Fatal("checkpoint timestamp is not fresh")
	}
}
