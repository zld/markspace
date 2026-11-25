package pipeline

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"mediad/internal/checkpoint"
	"mediad/internal/fixture"
	"mediad/internal/provenance"
	"mediad/internal/stream"
)

func readCP(dir string) (*checkpoint.State, error) {
	return checkpoint.NewStore(filepath.Join(dir, "checkpoint.json")).Load()
}

func TestDuplicateSegmentsKeepAllSourceRecords(t *testing.T) {
	fx, err := fixture.Generate(fixture.Spec{
		Codec: stream.H264, Frames: 2, IndexMode: fixture.IndexExact})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	frame0 := fx.Stream[:fx.FrameOffsets[1]]
	opts := Options{
		FixtureID: "dup", Codec: stream.H264,
		Stream:    bytes.Repeat(frame0, 3), // three byte-identical AUs
		IndexText: fx.IndexText, WorkDir: t.TempDir(),
		QueueSize: 1, StreamCfg: stream.DefaultConfig(),
	}
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(res.Segments))
	}
	m, err := provenance.Load(res.ManifestPath)
	if err != nil {
		t.Fatalf("Load manifest: %v", err)
	}
	if m.Segments[0].Duplicate || !m.Segments[1].Duplicate || !m.Segments[2].Duplicate {
		t.Fatalf("duplicate relation wrong: %+v", m.Segments)
	}
	if m.Segments[1].FirstOfHash != 0 || m.Segments[2].FirstOfHash != 0 {
		t.Fatalf("duplicates must reference the origin record 0: %+v", m.Segments)
	}
	// All three source records are retained, even though hashes coincide.
	if len(m.Segments) != 3 || m.Segments[0].SHA256 != m.Segments[2].SHA256 {
		t.Fatal("source records collapsed")
	}
}

func TestCorruptReasonsReachManifest(t *testing.T) {
	fx, err := fixture.Generate(fixture.Spec{
		Codec: stream.HEVC, Frames: 8, IndexMode: fixture.IndexExact, Truncate: 6})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	opts := Options{
		FixtureID: "corrupt", Codec: stream.HEVC,
		Stream: fx.Stream, IndexText: fx.IndexText, WorkDir: t.TempDir(),
		QueueSize: 1, StreamCfg: stream.DefaultConfig(),
	}
	res, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CorruptSegmentCount != 1 {
		t.Fatalf("corrupt count = %d", res.CorruptSegmentCount)
	}
	m, err := provenance.Load(res.ManifestPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found bool
	for _, s := range m.Segments {
		if s.Corrupt {
			found = true
			if len(s.Reasons) == 0 {
				t.Fatal("corrupt segment persisted without reasons")
			}
		}
	}
	if !found {
		t.Fatal("no corrupt segment in manifest")
	}
}

func TestCancellationStopsStages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	opts := baseOpts(t, t.TempDir(), fixture.Spec{
		Codec: stream.H264, Frames: 30, IndexMode: fixture.IndexExact})
	opts.QueueSize = 1
	opts.Stream = bytes.Repeat(opts.Stream, 5) // enough work to still be running
	cancel()                                   // pre-cancel: every stage must unwind cleanly
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, opts)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled run must surface an error rather than succeed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not exit after cancellation")
	}
}
