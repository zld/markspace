package index_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"replaycheck/internal/clock"
	"replaycheck/internal/index"
)

func TestWriteIsDeterministic(t *testing.T) {
	idx := index.Index{
		BasisVersion: clock.BasisVersion, InputCount: 1,
		Inputs:          []index.InputSummary{{Path: "a.mp4", Size: 4, SHA256: "deadbeef"}},
		CompletedBlocks: []index.BlockRecord{{Key: "a", Sequence: 1, StartNS: 0, EndNS: 10}},
		Anomalies:       []clock.Anomaly{},
	}
	p1 := filepath.Join(t.TempDir(), "a", "index.json")
	p2 := filepath.Join(t.TempDir(), "b", "index.json")
	h1, err := index.Write(p1, idx)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := index.Write(p2, idx)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("digests differ: %s != %s", h1, h2)
	}
	b, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex(b); got != h1 {
		t.Fatalf("returned hash %s does not match file hash %s", h1, got)
	}
}

func TestCheckpointVerifyRejectsChangedInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	inputs := []index.InputSummary{{Path: "a.mp4", Size: 1, SHA256: "one"}}
	cp := index.Checkpoint{CheckpointEvery: 4, Inputs: inputs, CompletedCount: 4}
	if err := index.SaveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := index.LoadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.VerifyInputs(loaded, inputs); err != nil {
		t.Fatalf("same inputs should resume: %v", err)
	}
	changed := []index.InputSummary{{Path: "a.mp4", Size: 2, SHA256: "two"}}
	err = index.VerifyInputs(loaded, changed)
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Code != clock.CodeDigestMismatch {
		t.Fatalf("changed input error = %v, want digest mismatch", err)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}
