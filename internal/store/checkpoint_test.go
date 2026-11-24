package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointLifecycle(t *testing.T) {
	dir := t.TempDir()
	cp, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Stages) != 0 {
		t.Fatal("missing checkpoint must load empty")
	}
	state := StageState{
		StageID:    "vid-a/verify",
		InputKey:   "in1",
		ParamsHash: "p1",
		Version:    "v1",
		CacheKey:   "key1",
		Digest:     "deadbeef",
	}
	if err := cp.MarkDone(dir, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "checkpoint.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp staging file must not remain after atomic save")
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Reusable("vid-a/verify", "key1")
	if !ok || got.Digest != "deadbeef" {
		t.Fatalf("reusable lookup failed: %+v", got)
	}
	if _, ok := reloaded.Reusable("vid-a/verify", "stale"); ok {
		t.Fatal("different cache key must invalidate the stage")
	}
	if reloaded.Digest("vid-a/verify") != "deadbeef" {
		t.Fatal("digest lookup failed")
	}
}

func TestCorruptCheckpointRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("corrupt checkpoint must surface an error, not be silently reset")
	}
}

func TestAtomicSaveLeavesPriorOnFailure(t *testing.T) {
	dir := t.TempDir()
	cp := &Checkpoint{RunKey: "run-1", Stages: map[string]StageState{}}
	if err := cp.MarkDone(dir, StageState{StageID: "s1", CacheKey: "k1", Version: "v1", Digest: "d1"}); err != nil {
		t.Fatal(err)
	}
	// Occupy the temp name with a directory so Save fails before rename;
	// the previously committed checkpoint must remain intact.
	if err := os.Mkdir(Path(dir)+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, cp); err == nil {
		t.Fatal("expected write failure when temp path is a directory")
	}
	got, loadErr := Load(dir)
	if loadErr != nil || got.Digest("s1") != "d1" {
		t.Fatalf("prior checkpoint not preserved: %v %v", got, loadErr)
	}
}

func TestInterruptedProcessResumesFromLastStage(t *testing.T) {
	dir := t.TempDir()
	// Simulate a process killed after stage 1: only stage 1 is on disk.
	cp1 := &Checkpoint{RunKey: "run-x", Stages: map[string]StageState{}}
	if err := cp1.MarkDone(dir, StageState{StageID: "s1", CacheKey: "k1", Version: "v1", Digest: "dig1"}); err != nil {
		t.Fatal(err)
	}
	// A fresh process loads the checkpoint and finds stage 1 reusable.
	cp2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cp2.Reusable("s1", "k1"); !ok {
		t.Fatal("completed stage must be reusable after restart")
	}
	if _, ok := cp2.Reusable("s2", "k2"); ok {
		t.Fatal("never-run stage must not appear reusable")
	}
}

func TestCacheKeyDeterminism(t *testing.T) {
	// Order-independent input identity.
	k1 := InputKey([]string{"a", "b", "c"})
	k2 := InputKey([]string{"c", "a", "b"})
	if k1 != k2 {
		t.Fatal("InputKey must be order-independent")
	}
	ph, err := ParamsHash(map[string]interface{}{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	ph2, _ := ParamsHash(map[string]interface{}{"a": 1, "b": 2})
	if ph != ph2 {
		t.Fatal("ParamsHash must be stable across map ordering")
	}
	ck1 := CacheKey("stage", k1, ph, "v1")
	ck2 := CacheKey("stage", k1, ph, "v1")
	ck3 := CacheKey("stage", k1, ph, "v2")
	if ck1 != ck2 || ck1 == ck3 {
		t.Fatal("cache keys must match on identical inputs and change with version")
	}
}
