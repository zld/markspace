package batch

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediapack/internal/fault"
	"mediapack/internal/index"
	"mediapack/internal/maptrack"
	"mediapack/internal/metrics"
	"mediapack/internal/remux"
	"mediapack/internal/timeline"
)

// Run executes one deterministic batch. Mapping, timeline and hash errors are
// fatal for the batch. Damaged input regions are collected and isolated while
// other inputs/tracks continue.
func Run(cfg Config) (*Result, error) {
	if cfg.OutDir == "" {
		return nil, fault.New(fault.KindArgument, "batch output directory is required")
	}
	if cfg.ChunkSamples <= 0 {
		cfg.ChunkSamples = 256
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, fault.Wrap(fault.KindPath, err)
	}
	if _, err := SaveConfig(cfg); err != nil {
		return nil, err
	}

	loaded := loadInputs(cfg)
	table, err := maptrack.Apply(loaded.catalog, cfg.Rules)
	if err != nil {
		return nil, err
	}
	tl, err := timeline.Assemble(table, cfg.Timeline)
	if err != nil {
		return nil, err
	}
	built := buildOutputs(table, tl)
	if built.totalSamples == 0 {
		fe := fault.New(fault.KindUnrecoverable, "no valid samples after fault isolation")
		return &Result{Unrecoverable: loaded.unrecoverable}, fe
	}

	storeRoot := filepath.Join(cfg.OutDir, "index")
	store, err := index.Open(storeRoot)
	if err != nil {
		return nil, err
	}
	records, replayed, err := commitTimelineChunks(store, table, tl, cfg.ChunkSamples)
	if err != nil {
		return nil, err
	}
	_ = replayed // cumulative recovery count is persisted on the checkpoint
	cp := store.Checkpoint()
	if cp.TotalSamples != built.totalSamples {
		return nil, fault.New(fault.KindCheckpoint,
			"checkpoint sample total %d != manifest total %d (possible double commit)",
			cp.TotalSamples, built.totalSamples)
	}

	outPath := filepath.Join(cfg.OutDir, "output.mp4")
	if _, err := remux.WriteMP4(outPath, uint32(tl.Timebase.Den), built.tracks); err != nil {
		return nil, err
	}

	target := cfg.TargetBitrateBps
	actual := metrics.BitrateBps(built.totalBytes, built.maxDuration)
	if target <= 0 {
		// Auto target is rounded to kb/s; this keeps the numerical contract
		// explicit when the caller does not supply a target.
		target = float64(int64(actual/1000+0.5)) * 1000
	}
	dev := 0.0
	if target > 0 {
		dev = absFloat(target-actual) / target * 100
	}
	trans := make([]any, 0, len(tl.Transitions))
	for _, tr := range tl.Transitions {
		trans = append(trans, tr)
	}
	manifest := index.Manifest{
		Version:                  1,
		OutputMP4:                "output.mp4",
		PayloadHash:              built.aggregateHash,
		Tracks:                   built.manifests,
		Transitions:              trans,
		SampleCount:              built.totalSamples,
		TargetBitrateBps:         target,
		ActualBitrateBps:         actual,
		BitrateDeviationPct:      dev,
		QualityDroppedSamples:    0,
		ManifestAlignmentErrorMS: 0,
		RecoveredChunks:          cp.RecoveredChunks,
		CommittedChunks:          cp.LastSequence,
		ErrorRanges:              loaded.errorRanges,
		Chunks:                   records,
	}
	if _, err := index.SaveJSON(cfg.OutDir, "manifest.json", manifest); err != nil {
		return nil, err
	}
	return &Result{Manifest: &manifest, Unrecoverable: loaded.unrecoverable}, nil
}

// SimulateLostLastCheckpoint removes the highest immutable checkpoint file.
// The atomic pointer is intentionally left behind so Open must fall back from
// the missing referenced checkpoint to the newest valid one.
func SimulateLostLastCheckpoint(outDir string) (int, error) {
	dir := filepath.Join(outDir, "index", "checkpoints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fault.Wrap(fault.KindCheckpoint, err)
	}
	last := -1
	var name string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		seq, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil && seq > last {
			last = seq
			name = e.Name()
		}
	}
	if last < 0 {
		return 0, fault.New(fault.KindCheckpoint, "no checkpoint to delete")
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		return 0, fault.Wrap(fault.KindCheckpoint, err)
	}
	return last, nil
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
