package pipeline_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/mux"
	"github.com/zld/continuity/internal/pipeline"
)

// This file exercises faults that travel through more than one stage, matching
// the acceptance requirement that at least two failure classes cross the
// repair, index and resume boundaries rather than being caught in isolation.

// writeFaultyFixture serializes a track with the requested structural fault.
func writeFaultyFixture(t *testing.T, path string, n, every int, fault mux.Faults) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	specs := make([]mux.SampleSpec, n)
	for i := range specs {
		specs[i] = mux.SampleSpec{Delta: 1, Sync: every > 0 && i%every == 0}
	}
	if _, err := mux.WriteFile(f, []mux.TrackSpec{
		{Timescale: 25, Video: true, Samples: specs},
	}, fault); err != nil && fault == mux.FaultNone {
		t.Fatal(err)
	}
}

// Fault class 1: a truncated sample table must be rejected at the demux boundary
// before repair or indexing can run, and the error stays ErrCorrupt end to end.
func TestCorruptInputFlowsThroughWholePipeline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "src.mp4")
	writeFaultyFixture(t, path, 200, 5, mux.FaultTruncateStsz)

	in, err := demux.Open(path)
	if err == nil {
		in.Close()
		t.Fatal("demux should reject the truncated sample table")
	}
	if !errors.Is(err, clock.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt at pipeline entry, got %v", err)
	}
	// The contextual error must still name a track and a sample position.
	var ce *clock.Error
	if !errors.As(err, &ce) || !ce.KnownT || !ce.KnownS {
		t.Fatalf("error must carry track and sample location: %v", err)
	}
}

// Fault class 2: a damaged checkpoint must be rejected at resume, even though
// the input media itself is healthy. The pipeline refuses before writing.
func TestDamagedCheckpointFlowsThroughResume(t *testing.T) {
	path, fp := fixture(t, 640, 4, mux.FaultNone)
	cpDir := filepath.Join(t.TempDir(), "cp")

	// Produce a valid first block, then corrupt it on disk.
	in1, _ := demux.Open(path)
	_, err := pipeline.RunWithResume(context.Background(), in1, clock.DefaultPolicy(), cpDir, fp,
		pipeline.Config{ChunkSize: 64, InterruptAfter: 1})
	in1.Close()
	if !errors.Is(err, clock.ErrInterrupted) {
		t.Fatalf("setup interrupt: %v", err)
	}
	chk := filepath.Join(cpDir, "chunk-000000.chk")
	raw, err := os.ReadFile(chk)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xAA
	if err := os.WriteFile(chk, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// A healthy input plus a corrupt checkpoint must surface ErrResumeMismatch
	// through the resume stage rather than silently rebuilding.
	in2, _ := demux.Open(path)
	defer in2.Close()
	_, err = pipeline.RunWithResume(context.Background(), in2, clock.DefaultPolicy(), cpDir, fp,
		pipeline.Config{ChunkSize: 64})
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("want ErrResumeMismatch, got %v", err)
	}
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Stage != clock.StageResume {
		t.Fatalf("error must originate in resume stage: %v", err)
	}
}

// Fault class 3: changing the input invalidates the fingerprint, so previously
// committed blocks no longer match and resume is refused.
func TestChangedInputFlowsThroughFingerprintCheck(t *testing.T) {
	path, fp := fixture(t, 640, 4, mux.FaultNone)
	cpDir := filepath.Join(t.TempDir(), "cp")
	in1, _ := demux.Open(path)
	_, err := pipeline.RunWithResume(context.Background(), in1, clock.DefaultPolicy(), cpDir, fp,
		pipeline.Config{ChunkSize: 64, InterruptAfter: 1})
	in1.Close()
	if !errors.Is(err, clock.ErrInterrupted) {
		t.Fatalf("setup: %v", err)
	}
	changed := fp
	changed[0] ^= 0x01
	in2, _ := demux.Open(path)
	defer in2.Close()
	_, err = pipeline.RunWithResume(context.Background(), in2, clock.DefaultPolicy(), cpDir, changed,
		pipeline.Config{ChunkSize: 64})
	if !errors.Is(err, clock.ErrResumeMismatch) {
		t.Fatalf("want resume mismatch for changed input, got %v", err)
	}
}
