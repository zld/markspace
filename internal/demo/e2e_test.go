package demo

import (
	"path/filepath"
	"testing"

	"continuity/internal/index"
	"continuity/internal/pipeline"
	"continuity/internal/wav"
)

// TestEndToEndAcceptance drives the full pipeline exactly like the
// command adapter and asserts every quantitative acceptance contract.
func TestEndToEndAcceptance(t *testing.T) {
	dir := t.TempDir()
	arrival := Generate(17, DefaultPlan())
	blocks, stats, align, err := Rebuild(arrival)
	if err != nil {
		t.Fatal(err)
	}
	layout, _ := wav.NewLayout(Channels, Bits, SampleRate)
	inputs := make([]pipeline.BlockInput, 0, len(blocks))
	for _, b := range blocks {
		inputs = append(inputs, pipeline.BlockInput{
			Block: b, Layout: layout, AlignmentSamples: align,
		})
	}
	cfg := pipeline.Config{
		WorkDir: filepath.Join(dir, "pipeline"), BlockFrames: BlockFrames,
		FramesPerBlock: 4, FrameWidth: 16, FrameHeight: 16,
		ContainerDigest: "container-hash",
		ContainerMeta:   index.ContainerRecord{Hash: "container-hash", Timescale: SampleRate},
	}
	r, err := pipeline.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.Run(inputs, stats)
	if err != nil {
		t.Fatal(err)
	}

	// Fixed fault injection contract.
	if m.Duplicates != 1 {
		t.Errorf("duplicates=%d want 1", m.Duplicates)
	}
	if m.Reordered <= 0 {
		t.Errorf("reordered=%d want >0", m.Reordered)
	}
	if m.AlignmentErrorSmp > 1 {
		t.Errorf("alignment error=%d want <=1", m.AlignmentErrorSmp)
	}
	if m.PeakDBFS > 0 || m.PeakDBFS < -60 {
		t.Errorf("peak_dbfs=%f outside [-60,0]", m.PeakDBFS)
	}
	if m.Blocks != len(blocks) {
		t.Errorf("blocks=%d want %d", m.Blocks, len(blocks))
	}

	// Repeat run: identical hashes and full reuse.
	r2, err := pipeline.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := r2.Run(inputs, stats)
	if err != nil {
		t.Fatal(err)
	}
	if m2.PCMHash != m.PCMHash || m2.SegmentHash != m.SegmentHash {
		t.Fatal("repeated run changed hashes")
	}
	if m2.Reused <= 0 || m2.Recomputed != 0 {
		t.Fatalf("repeat run reuse wrong: reused=%d recomputed=%d", m2.Reused, m2.Recomputed)
	}
}
