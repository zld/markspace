package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/model"
	"mediasync/internal/runner"
	"mediasync/internal/rtp"
)

func TestTaskKeyStableAndDistinct(t *testing.T) {
	k1 := runner.TaskKey("input-a", "params-1")
	k1Again := runner.TaskKey("input-a", "params-1")
	if k1 != k1Again {
		t.Fatal("identical inputs/params must map to a stable task key")
	}
	if runner.TaskKey("input-b", "params-1") == k1 {
		t.Fatal("changed input must produce a different task key")
	}
	if runner.TaskKey("input-a", "params-2") == k1 {
		t.Fatal("changed params must produce a different task key")
	}
}

func TestCheckpointRoundTripAndGuard(t *testing.T) {
	dir := t.TempDir()
	params := map[string]any{"window": 8, "clock": 90000}
	pd, err := runner.DigestParams(params)
	if err != nil {
		t.Fatal(err)
	}
	pdReordered, err := runner.DigestParams(map[string]any{"clock": 90000, "window": 8})
	if err != nil {
		t.Fatal(err)
	}
	if pd != pdReordered {
		t.Fatal("param digest must be order-independent")
	}
	key := runner.TaskKey("sha-input", pd)
	st := rtp.Stats{SSRC: 7, Duplicates: 2, Late: 1}
	cp := runner.FromStats(key, "sha-input", pd, st, 100, 105, 90, 95,
		[]runner.Segment{{StartExt: 90, EndExt: 104, Count: 15}},
		[]uint64{102}, []uint64{90, 91, 92})
	if err := runner.SaveCheckpoint(dir, cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := runner.LoadCheckpoint(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CommittedCount != 95 || loaded.NextCommitExt != 100 {
		t.Fatalf("checkpoint state lost: %+v", loaded)
	}
	if err := runner.Guard(loaded, "sha-input", pd); err != nil {
		t.Fatalf("matching checkpoint must pass guard: %v", err)
	}
	if err := runner.Guard(loaded, "sha-other", pd); model.AsMediaError(err) == nil {
		t.Fatal("foreign input digest must be rejected")
	}
	if err := runner.Guard(loaded, "sha-input", "other-params"); model.AsMediaError(err) == nil {
		t.Fatal("foreign params must be rejected")
	}
}

func TestCheckpointCorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	pd, _ := runner.DigestParams(map[string]any{"w": 4})
	key := runner.TaskKey("in", pd)
	cp := runner.FromStats(key, "in", pd, rtp.Stats{}, 1, 1, 0, 1, nil, nil, nil)
	if err := runner.SaveCheckpoint(dir, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "checkpoints", key+".json"))
	_ = os.WriteFile(filepath.Join(dir, "checkpoints", key+".json"), raw[:len(raw)/2], 0o644)
	if _, err := runner.LoadCheckpoint(dir, key); model.AsMediaError(err) == nil ||
		model.AsMediaError(err).Class != model.ClassCheckpoint {
		t.Fatalf("truncated checkpoint must be a structured checkpoint error")
	}
}

func TestMissingCheckpointIsFreshStart(t *testing.T) {
	cp, err := runner.LoadCheckpoint(t.TempDir(), "never-existed")
	if err != nil || cp != nil {
		t.Fatalf("missing checkpoint must be (nil,nil), got %v %v", cp, err)
	}
}
