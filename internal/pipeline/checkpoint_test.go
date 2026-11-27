package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediakernel/internal/cache"
	"mediakernel/internal/clock"
)

func testStore(t *testing.T) (*CheckpointStore, string) {
	t.Helper()
	base := t.TempDir()
	s, err := NewCheckpointStore(base, filepath.Join("run", "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s, base
}

func sampleCP() *Checkpoint {
	return &Checkpoint{
		InputDigest:  "in",
		ParamsDigest: "params",
		Stages: []StageState{
			{Name: "parse", Version: "v1", InputDigest: "in", ParamsDigest: "params", ArtifactDigest: "a1"},
			{Name: "index", Version: "v1", InputDigest: "in", ParamsDigest: "params", ArtifactDigest: "a2"},
			{Name: "report", Version: "v1", InputDigest: "in", ParamsDigest: "params", ArtifactDigest: "a3"},
		},
	}
}

func TestCheckpointSaveLoadRoundTrip(t *testing.T) {
	s, _ := testStore(t)
	cp := sampleCP()
	if err := s.Save(cp); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stages) != 3 || got.Stages[2].ArtifactDigest != "a3" {
		t.Fatalf("loaded checkpoint wrong: %+v", got)
	}
}

func TestMissingCheckpointIsFreshRun(t *testing.T) {
	s, _ := testStore(t)
	cp, err := s.Load()
	if err != nil || cp != nil {
		t.Fatalf("missing file must be nil/nil, got %v %v", cp, err)
	}
}

func TestCorruptCheckpointRejected(t *testing.T) {
	s, _ := testStore(t)
	if err := os.WriteFile(s.path, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatCheckpoint {
		t.Fatalf("want checkpoint_corrupt, got %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if cp, err := s.Load(); err != nil || cp != nil {
		t.Fatal("after removal a fresh run should see no checkpoint")
	}
}

func TestCorruptStageFieldRejected(t *testing.T) {
	s, _ := testStore(t)
	cp := sampleCP()
	cp.Stages[1].ArtifactDigest = ""
	if err := s.Save(cp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("empty artifact digest must be rejected")
	}
}

func TestReusableStagesPrefixOnly(t *testing.T) {
	cp := sampleCP()
	want := []StageState{
		{Name: "parse", Version: "v1", InputDigest: "in", ParamsDigest: "params", ArtifactDigest: "a1"},
		{Name: "index", Version: "v1", InputDigest: "in", ParamsDigest: "params", ArtifactDigest: "a2"},
		{Name: "report", Version: "v1", InputDigest: "in", ParamsDigest: "params", ArtifactDigest: "a3"},
	}
	if n := cp.ReusableStages(want); n != 3 {
		t.Fatalf("all matching = %d", n)
	}
	// A mid-pipeline mismatch must stop reuse before that stage.
	want[1].ArtifactDigest = "different"
	if n := cp.ReusableStages(want); n != 1 {
		t.Fatalf("prefix reuse = %d, want 1", n)
	}
	// Changed parameters invalidate everything.
	changed := want
	changed[0].ParamsDigest = "other"
	if n := cp.ReusableStages(changed); n != 0 {
		t.Fatalf("param change must invalidate, got %d", n)
	}
	var nilCP *Checkpoint
	if n := nilCP.ReusableStages(want); n != 0 {
		t.Fatal("nil checkpoint reuses nothing")
	}
}

func TestRepeatedResumeProducesSameRecord(t *testing.T) {
	s, _ := testStore(t)
	cp := sampleCP()
	for i := 0; i < 3; i++ {
		if err := s.Save(cp); err != nil {
			t.Fatal(err)
		}
		got, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got.Stages[2].ArtifactDigest != "a3" {
			t.Fatal("resume record drifted across repeated saves")
		}
	}
}

func TestParamsDigestStable(t *testing.T) {
	p := cache.Params{Channels: 2, SampleRate: 48000, BlockFrames: 480, StageVersion: "v1"}
	d1, d2 := ParamsDigest(p), ParamsDigest(p)
	if d1 != d2 || len(d1) != 64 {
		t.Fatal("params digest must be a stable sha-256 hex")
	}
}
