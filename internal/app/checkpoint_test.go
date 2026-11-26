package app

import (
	"os"
	"path/filepath"
	"testing"

	"rtpregress/internal/clock"
)

func TestCheckpointSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CheckpointFileName)
	cp := &Checkpoint{
		Schema: "rtp-regress/v1", InputSHA256: "abc", WorkerCount: 4,
		TargetRate: 48000, Channels: 1, Bitrate: 96000,
		Completed: []CompletedSegment{{SegNumber: 1, PID: 0x100, Samples: 1024,
			PCMSHA256: "deadbeef"}},
	}
	if err := cp.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCheckpoint(path, "abc")
	if err != nil || got == nil {
		t.Fatalf("load: %v", err)
	}
	if !got.HasSegment(1) || got.HasSegment(2) {
		t.Fatal("segment membership wrong")
	}
	if row, ok := got.FindSegment(1); !ok || row.PID != 0x100 {
		t.Fatal("find segment wrong")
	}
}

func TestCheckpointMissingReturnsNil(t *testing.T) {
	got, err := LoadCheckpoint(filepath.Join(t.TempDir(), "nope.json"), "x")
	if err != nil || got != nil {
		t.Fatalf("missing checkpoint must be (nil,nil), got %v %v", got, err)
	}
}

func TestCheckpointRejectsMismatchedInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CheckpointFileName)
	cp := &Checkpoint{Schema: "x", InputSHA256: "aaa"}
	if err := cp.Save(path); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCheckpoint(path, "bbb")
	if e := clock.AsError(err); e == nil || e.Code != clock.CodeCheckpoint {
		t.Fatalf("want CodeCheckpoint, got %v", err)
	}
}

func TestCheckpointRejectsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CheckpointFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path, "x"); err == nil {
		t.Fatal("corrupt checkpoint must error")
	}
}

func TestRunHashDeterministic(t *testing.T) {
	rows := []CompletedSegment{
		{SegNumber: 0, StartSample: 0, PCMSHA256: "a"},
		{SegNumber: 1, StartSample: 48, PCMSHA256: "b"},
	}
	h1 := ComputeRunHash(rows)
	// same content, different slice capacity/order-independent coordinates
	h2 := ComputeRunHash([]CompletedSegment{rows[0], rows[1]})
	if h1 != h2 || h1 == "" {
		t.Fatal("run hash must be stable for identical ordered rows")
	}
	h3 := ComputeRunHash([]CompletedSegment{
		{SegNumber: 0, StartSample: 0, PCMSHA256: "a"},
		{SegNumber: 1, StartSample: 49, PCMSHA256: "b"},
	})
	if h3 == h1 {
		t.Fatal("changed coordinates must change the hash")
	}
}
