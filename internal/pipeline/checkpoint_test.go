package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointRoundTripAndResumeStage(t *testing.T) {
	dir := t.TempDir()
	cp := newCheckpoint("cfg", "in")
	if cp.ResumeStage() != "parse" {
		t.Fatalf("fresh resume stage = %s", cp.ResumeStage())
	}
	if err := cp.Mark(dir, "parse", "hash1"); err != nil {
		t.Fatal(err)
	}
	cp, ok := LoadCheckpoint(dir, "cfg", "in")
	if !ok {
		t.Fatal("checkpoint not loaded")
	}
	if cp.ResumeStage() != "jitter" {
		t.Fatalf("resume stage = %s", cp.ResumeStage())
	}
	if err := cp.Mark(dir, "jitter", "hash2"); err != nil {
		t.Fatal(err)
	}
	if err := cp.Mark(dir, "recovery", "hash3"); err != nil {
		t.Fatal(err)
	}
	cp, ok = LoadCheckpoint(dir, "cfg", "in")
	if !ok || cp.ResumeStage() != "sync" {
		t.Fatalf("resume = %+v ok=%v", cp, ok)
	}
	if cp.Hashes["jitter"] != "hash2" {
		t.Fatalf("hashes = %+v", cp.Hashes)
	}
}

func TestCheckpointInvalidatesOnConfigOrInputChange(t *testing.T) {
	dir := t.TempDir()
	cp := newCheckpoint("cfg", "in")
	for _, stage := range []string{"parse", "jitter", "recovery", "sync", "mux", "index", "manifest"} {
		if err := cp.Mark(dir, stage, stage+"-hash"); err != nil {
			t.Fatal(err)
		}
	}
	if cp.ResumeStage() != "complete" {
		t.Fatalf("complete resume = %s", cp.ResumeStage())
	}
	if _, ok := LoadCheckpoint(dir, "cfg2", "in"); ok {
		t.Fatal("config change reused checkpoint")
	}
	if _, ok := LoadCheckpoint(dir, "cfg", "in2"); ok {
		t.Fatal("input change reused checkpoint")
	}
	stale, ok := LoadCheckpoint(dir, "cfg2", "in")
	if ok || len(stale.Done) != 0 {
		t.Fatalf("stale checkpoint kept stages: %+v", stale)
	}
}

func TestCheckpointRejectsUnknownStageAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	cp := newCheckpoint("c", "i")
	if err := cp.Mark(dir, "bogus", "h"); err == nil {
		t.Fatal("unknown stage accepted")
	}
	os.WriteFile(filepath.Join(dir, checkpointName), []byte("{not json"), 0o644)
	fresh, ok := LoadCheckpoint(dir, "c", "i")
	if ok {
		t.Fatal("corrupt checkpoint accepted")
	}
	if fresh.ResumeStage() != "parse" {
		t.Fatalf("fresh = %s", fresh.ResumeStage())
	}
}

func TestStageHashSensitivity(t *testing.T) {
	a := stageHash("mux", "cfg", "in1", "in2")
	if a != stageHash("mux", "cfg", "in1", "in2") {
		t.Fatal("stage hash unstable")
	}
	if a == stageHash("mux", "cfg2", "in1", "in2") {
		t.Fatal("config change did not change hash")
	}
	if a == stageHash("mux", "cfg", "in1", "in3") {
		t.Fatal("input change did not change hash")
	}
	if a == stageHash("index", "cfg", "in1", "in2") {
		t.Fatal("stage change did not change hash")
	}
	// Hex digests and plain strings must not collide.
	if stageHash("s", "c", "abc") == stageHash("s", "c") {
		t.Fatal("input presence not encoded")
	}
}

func TestAtomicWriteNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := atomicWrite(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "two" {
		t.Fatalf("content = %q err=%v", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("stray files: %d", len(entries))
	}
}
