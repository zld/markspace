package task

import (
	"audionorm/internal/audio"
	"audionorm/internal/media"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testSpec() Spec {
	return Spec{SampleRate: 48000, Channels: 2, TargetRMS: -18, PeakCeilDB: -1}
}

func newCheckpoint() *Checkpoint {
	r := media.Range{Start: 0, End: 48000}
	key := StableKey("stream-1", r, testSpec())
	return &Checkpoint{
		TaskKey: key, StreamID: "stream-1", Range: r, Spec: testSpec(),
		Stage: StageNew, Status: StatusPASS,
		InputSHA256:    HashBytes([]byte("input-bytes")),
		DecoderVersion: audio.DecoderVersion,
		Stages:         map[Stage]*StageResult{},
	}
}

func TestStageOrdering(t *testing.T) {
	if !CanAdvance(StageNew, StageIngest) || CanAdvance(StageNew, StageSeal) {
		t.Fatal("stage advancement rules broken")
	}
	if Index(StageNormalize) != 3 {
		t.Fatal("stage index wrong")
	}
}

func TestStableKeyDeterministic(t *testing.T) {
	r := media.Range{Start: 100, End: 200}
	k1 := StableKey("s", r, testSpec())
	k2 := StableKey("s", r, testSpec())
	k3 := StableKey("other", r, testSpec())
	if k1 != k2 || len(k1) != 16 || k1 == k3 {
		t.Fatal("stable key must be deterministic per inputs and distinct per stream")
	}
}

func TestCheckpointSaveLoadAndResume(t *testing.T) {
	root := t.TempDir()
	cp := newCheckpoint()
	if err := cp.RecordStage(root, StageIngest, "ingest.json", []byte(`{"packets":10}`)); err != nil {
		t.Fatal(err)
	}
	if err := cp.RecordStage(root, StageDecode, "decode.f32", []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}

	// Simulate restart: load and verify; both stages must be reusable.
	loaded, err := LoadCheckpoint(root, cp.TaskKey)
	if err != nil {
		t.Fatal(err)
	}
	reusable, err := loaded.Verify(root)
	if err != nil || reusable != StageDecode {
		t.Fatalf("resume verify: reusable=%s err=%v", reusable, err)
	}
}

func TestVerifyDetectsTamperedPayload(t *testing.T) {
	root := t.TempDir()
	cp := newCheckpoint()
	if err := cp.RecordStage(root, StageIngest, "ingest.json", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(TaskDir(root, cp.TaskKey), "ingest.json")
	if err := os.WriteFile(payloadPath, []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadCheckpoint(root, cp.TaskKey)
	reusable, err := loaded.Verify(root)
	if !errors.Is(err, media.ErrSentinelCheckpoint) || reusable != StageNew {
		t.Fatalf("tamper must invalidate to new: reusable=%s err=%v", reusable, err)
	}
}

func TestInputChangeAndConflict(t *testing.T) {
	root := t.TempDir()
	cp := newCheckpoint()
	if err := cp.RecordStage(root, StageIngest, "ingest.json", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadCheckpoint(root, cp.TaskKey)
	if !loaded.InputChanged(HashBytes([]byte("different-input"))) {
		t.Fatal("changed input must be detected")
	}
	err := CheckConflict(loaded, "stream-1", loaded.Range, loaded.Spec, HashBytes([]byte("different-input")))
	if !errors.Is(err, media.ErrSentinelConflict) {
		t.Fatalf("same key different input must conflict, got %v", err)
	}
	// Identical input resumes without conflict.
	if err := CheckConflict(loaded, "stream-1", loaded.Range, loaded.Spec, loaded.InputSHA256); err != nil {
		t.Fatalf("identical input must not conflict: %v", err)
	}
}

func TestIllegalStageTransitionRejected(t *testing.T) {
	root := t.TempDir()
	cp := newCheckpoint()
	if err := cp.RecordStage(root, StageSeal, "seal.json", []byte(`{}`)); err == nil {
		t.Fatal("new -> seal must be rejected")
	}
}

func TestAtomicWriteNoHalfFiles(t *testing.T) {
	root := t.TempDir()
	hash1, err := AtomicWrite(root, "manifest.json", map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := AtomicWrite(root, "manifest.json", map[string]int{"a": 1})
	if err != nil || hash1 != hash2 {
		t.Fatal("identical content must hash identically")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
