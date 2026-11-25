package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/index"
	"continuity/internal/rtp"
	"continuity/internal/wav"
)

func makeBlockInputs(n int, mutate int) []BlockInput {
	layout, _ := wav.NewLayout(2, 16, 48000)
	frameBytes := int64(4)
	out := make([]BlockInput, 0, n)
	for i := 0; i < n; i++ {
		pcm := make([]byte, 16*frameBytes)
		for j := range pcm {
			pcm[j] = byte(i*17 + j)
		}
		if i == mutate {
			pcm[0] ^= 0xAA
		}
		out = append(out, BlockInput{
			Block: rtp.Block{
				Index: i, StartSample: int64(i) * 16, EndSample: int64(i+1) * 16,
				PCM: pcm,
			},
			Layout:           layout,
			AlignmentSamples: 0,
		})
	}
	return out
}

func stats() rtp.Stats { return rtp.Stats{Duplicates: 1, Reordered: 2, Gaps: 1} }

func TestRunRecomputesThenReuses(t *testing.T) {
	dir := t.TempDir()
	r, err := New(Config{WorkDir: filepath.Join(dir, "run"), BlockFrames: 16, FramesPerBlock: 2, FrameWidth: 8, FrameHeight: 8})
	if err != nil {
		t.Fatal(err)
	}
	blocks := makeBlockInputs(4, -1)
	m1, err := r.Run(blocks, stats())
	if err != nil {
		t.Fatal(err)
	}
	if m1.Recomputed != 4 || m1.Reused != 0 || m1.Blocks != 4 {
		t.Fatalf("first run: %+v", m1)
	}

	// Second identical run: every block is reused, hashes identical.
	r2, err := New(Config{WorkDir: filepath.Join(dir, "run"), BlockFrames: 16, FramesPerBlock: 2, FrameWidth: 8, FrameHeight: 8})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := r2.Run(blocks, stats())
	if err != nil {
		t.Fatal(err)
	}
	if m2.Reused != 4 || m2.Recomputed != 0 {
		t.Fatalf("second run reuse wrong: reused=%d recomputed=%d", m2.Reused, m2.Recomputed)
	}
	if m2.PCMHash != m1.PCMHash || m2.SegmentHash != m1.SegmentHash {
		t.Fatal("repeated run changed hashes")
	}
}

func TestSingleBlockChangeInvalidatesOnlyDependents(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(Config{WorkDir: filepath.Join(dir, "run"), BlockFrames: 16, FramesPerBlock: 1, FrameWidth: 8, FrameHeight: 8})
	base := makeBlockInputs(4, -1)
	r.Run(base, rtp.Stats{})

	// Mutate block 2 only.
	changed := makeBlockInputs(4, 2)
	r2, _ := New(Config{WorkDir: filepath.Join(dir, "run"), BlockFrames: 16, FramesPerBlock: 1, FrameWidth: 8, FrameHeight: 8})
	m, err := r2.Run(changed, rtp.Stats{})
	if err != nil {
		t.Fatal(err)
	}
	// Blocks 0 and 1 reuse; block 2 changes and invalidates 3 too.
	if m.Reused != 2 {
		t.Fatalf("reused=%d want 2", m.Reused)
	}
	if m.Recomputed != 2 {
		t.Fatalf("recomputed=%d want 2 (block 2 and dependent 3)", m.Recomputed)
	}
}

func TestContainerChangeDoesNotRecomputePCM(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "run")
	blocks := makeBlockInputs(3, -1)
	cfg := Config{WorkDir: work, BlockFrames: 16, FramesPerBlock: 1, FrameWidth: 8, FrameHeight: 8}
	r, _ := New(cfg)
	r.Run(blocks, rtp.Stats{})

	// New run with changed container metadata but identical PCM.
	cfg.ContainerDigest = "container-A"
	cfg.ContainerMeta = index.ContainerRecord{Hash: "container-A", Timescale: 48000, Brand: "isom"}
	r2, _ := New(cfg)
	m, err := r2.Run(blocks, rtp.Stats{})
	if err != nil {
		t.Fatal(err)
	}
	if m.PCMRecomputed != 0 {
		t.Fatalf("PCM recomputed=%d want 0 on container-only change", m.PCMRecomputed)
	}
	if m.ContainerRecomputed != 1 {
		t.Fatalf("container recomputed=%d want 1", m.ContainerRecomputed)
	}
	if m.Reused != 3 {
		t.Fatalf("PCM segments should be reused: %d", m.Reused)
	}

	// Re-run with the same container: nothing recomputes.
	r3, _ := New(cfg)
	m3, _ := r3.Run(blocks, rtp.Stats{})
	if m3.ContainerRecomputed != 0 || m3.PCMRecomputed != 0 {
		t.Fatalf("stable container still recomputing: %+v", m3)
	}
}

func TestCheckpointRecoveryAfterCorruption(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "run")
	blocks := makeBlockInputs(4, -1)
	cfg := Config{WorkDir: work, BlockFrames: 16, FramesPerBlock: 1, FrameWidth: 8, FrameHeight: 8}
	r, _ := New(cfg)
	clean, err := r.Run(blocks, rtp.Stats{})
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt the checkpoint index.
	idxPath := filepath.Join(work, "index", "index.json")
	if err := os.WriteFile(idxPath, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2, err := New(cfg)
	if err != nil {
		t.Fatalf("corrupt checkpoint should recover, got %v", err)
	}
	recovered, err := r2.Run(blocks, rtp.Stats{})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.RollbackBlocks < 1 {
		t.Fatalf("rollback not reported: %+v", recovered)
	}
	if recovered.PCMHash != clean.PCMHash || recovered.SegmentHash != clean.SegmentHash {
		t.Fatal("recovered hashes differ from clean run")
	}
}

func TestManifestWrittenWithEvidence(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "run")
	r, _ := New(Config{WorkDir: work, BlockFrames: 16, FramesPerBlock: 2, FrameWidth: 8, FrameHeight: 8})
	blocks := makeBlockInputs(2, -1)
	blocks[0].Block.GapSamples = 4
	blocks[0].Block.GapRanges = []rtp.GapRange{{StartSample: 0, EndSample: 4}}
	m, err := r.Run(blocks, rtp.Stats{Gaps: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Segments[0].Gap {
		t.Fatal("manifest must mark input gap")
	}
	if _, err := os.Stat(filepath.Join(work, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "segments")); err != nil {
		t.Fatal("segments dir missing")
	}
}
