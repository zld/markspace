package batch

import (
	"errors"
	"os"
	"path/filepath"

	"replaycheck/internal/clock"
	"replaycheck/internal/fixture"
	"replaycheck/internal/index"
)

// Default thresholds from the task specification.
const (
	DefaultAnomalyRateThresholdPct     = 1.0
	DefaultBudgetDeviationThresholdPct = 2.0
)

// Config controls one offline batch invocation.
type Config struct {
	Samples                     []fixture.Sample
	IndexPath                   string
	CheckpointPath              string
	ChunkEvery                  int
	Resume                      bool
	InterruptKey                string
	AnomalyRateThresholdPct     float64
	BudgetDeviationThresholdPct float64
}

// Metrics is the aggregate budget and quality report.
type Metrics struct {
	Files              int     `json:"files"`
	TracksChecked      int     `json:"tracks_checked"`
	WrapCount          int     `json:"wrap_count"`
	MaxDriftMS         float64 `json:"drift_ms"`
	VFRBoundaryErrorMS float64 `json:"vfr_boundary_error_ms"`
	AnomalyRatePct     float64 `json:"anomaly_rate"`
	BudgetDeviationPct float64 `json:"budget_deviation_pct"`
	InputDurationNS    int64   `json:"input_duration_ns"`
	PlannedClipNS      int64   `json:"planned_clip_ns"`
	ActualClipNS       int64   `json:"actual_clip_ns"`
	AnomalyDurationNS  int64   `json:"anomaly_duration_ns"`
	RecomputedBlocks   int     `json:"recomputed_blocks"`
}

// Result contains the written index, digest, metrics, and real exit code.
type Result struct {
	Index            index.Index
	IndexSHA256      string
	Metrics          Metrics
	ExitCode         int
	RecomputedBlocks []string
	Checkpoint       *index.Checkpoint
}

// ErrInterrupted simulates a process killed before an unconfirmed block commits.
var ErrInterrupted = errors.New("batch interrupted before block checkpoint")

// Run processes samples as fixed checkpoint blocks. A resumed run verifies
// input digests and recomputes only keys absent from the checkpoint.
func Run(cfg Config) (*Result, error) {
	if cfg.ChunkEvery <= 0 {
		cfg.ChunkEvery = 4
	}
	if cfg.AnomalyRateThresholdPct == 0 {
		cfg.AnomalyRateThresholdPct = DefaultAnomalyRateThresholdPct
	}
	if cfg.BudgetDeviationThresholdPct == 0 {
		cfg.BudgetDeviationThresholdPct = DefaultBudgetDeviationThresholdPct
	}
	if cfg.CheckpointPath == "" {
		cfg.CheckpointPath = cfg.IndexPath + ".checkpoint.json"
	}

	inputs := make([]index.InputSummary, 0, len(cfg.Samples))
	for _, s := range cfg.Samples {
		sum, err := index.HashFile(s.Path)
		if err != nil {
			return &Result{ExitCode: 1}, clock.NewError(clock.CodeUnreadable, err.Error(), -1).WithPath(s.Path)
		}
		inputs = append(inputs, sum)
	}

	cp := &index.Checkpoint{
		BasisVersion:    clock.BasisVersion,
		CheckpointEvery: cfg.ChunkEvery,
		Inputs:          inputs,
	}
	checkpointExisted := false
	if cfg.Resume {
		loaded, err := index.LoadCheckpoint(cfg.CheckpointPath)
		switch {
		case err == nil:
			cp = loaded
			checkpointExisted = true
			if err := index.VerifyInputs(cp, inputs); err != nil {
				return &Result{ExitCode: 1, Checkpoint: cp}, err
			}
		case errors.Is(err, os.ErrNotExist):
			// A resume with no checkpoint is a fresh start.
		default:
			return &Result{ExitCode: 1}, err
		}
	}

	result := &Result{Checkpoint: cp}
	allAnomalies := append([]clock.Anomaly{}, cp.Anomalies...)
	conclusions := append([]index.FileConclusion{}, cp.Conclusions...)
	blocks := append([]index.BlockRecord{}, cp.CompletedBlocks...)

	for i, s := range cfg.Samples {
		if cp.IsConfirmed(s.Name) {
			continue
		}
		if cfg.InterruptKey == s.Name {
			// Process enough to prove the block is reachable, but die before
			// appending or checkpointing it.
			_ = processSample(s)
			return result, ErrInterrupted
		}

		p := processSample(s)
		p.block.Sequence = i + 1
		p.block.Tracks = p.tracks
		p.block.WrapCount = p.wrapCount
		p.block.MaxDriftNS = p.maxDriftNS
		p.block.MaxBoundaryNS = p.maxBoundaryNS
		p.block.InputDurationNS = p.inputDurationNS
		p.block.PlannedClipNS = p.plannedClipNS
		p.block.ActualClipNS = p.actualClipNS
		p.block.AnomalyDurationNS = p.anomalyDurationNS

		if checkpointExisted {
			result.RecomputedBlocks = append(result.RecomputedBlocks, s.Name)
		}
		blocks = append(blocks, p.block)
		conclusions = append(conclusions, p.conclusion)
		allAnomalies = append(allAnomalies, p.anomalies...)

		cp.CompletedBlocks = blocks
		cp.Conclusions = conclusions
		cp.Anomalies = allAnomalies
		cp.ConfirmedBlockKeys = append(cp.ConfirmedBlockKeys, s.Name)
		cp.LastBlockKey = s.Name
		cp.CompletedCount = len(blocks)

		if len(blocks)%cfg.ChunkEvery == 0 {
			if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
				return &Result{ExitCode: 1}, err
			}
		}
	}

	if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
		return &Result{ExitCode: 1}, err
	}

	idx := index.Index{
		BasisVersion:       clock.BasisVersion,
		InputCount:         len(inputs),
		Inputs:             inputs,
		CompletedBlocks:    blocks,
		Conclusions:        conclusions,
		Anomalies:          allAnomalies,
		AnomalyRate:        0,
		BudgetDeviationPct: 0,
	}
	result.Metrics = aggregate(cfg.Samples, blocks, result.RecomputedBlocks)
	idx.AnomalyRate = result.Metrics.AnomalyRatePct
	idx.BudgetDeviationPct = result.Metrics.BudgetDeviationPct
	result.Metrics.RecomputedBlocks = len(result.RecomputedBlocks)

	if cfg.IndexPath != "" {
		hash, err := index.Write(cfg.IndexPath, idx)
		if err != nil {
			return &Result{ExitCode: 1}, err
		}
		result.IndexSHA256 = hash
	}
	result.Index = idx
	result.ExitCode = decideExit(cfg, idx.Conclusions, result.Metrics)
	return result, nil
}

func saveCheckpoint(path string, cp *index.Checkpoint) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return index.SaveCheckpoint(path, *cp)
}

func aggregate(samples []fixture.Sample, blocks []index.BlockRecord, recomputed []string) Metrics {
	m := Metrics{Files: len(samples), RecomputedBlocks: len(recomputed)}
	var maxDriftNS, maxVFRNS int64
	for _, b := range blocks {
		m.TracksChecked += b.Tracks
		m.WrapCount += b.WrapCount
		m.InputDurationNS += b.InputDurationNS
		m.PlannedClipNS += b.PlannedClipNS
		m.ActualClipNS += b.ActualClipNS
		m.AnomalyDurationNS += b.AnomalyDurationNS
		if b.MaxDriftNS > maxDriftNS {
			maxDriftNS = b.MaxDriftNS
		}
		if b.MaxBoundaryNS > maxVFRNS {
			maxVFRNS = b.MaxBoundaryNS
		}
	}
	m.MaxDriftMS = float64(maxDriftNS) / float64(clock.NanosecondsPerMillisecond)
	m.VFRBoundaryErrorMS = float64(maxVFRNS) / float64(clock.NanosecondsPerMillisecond)
	if m.InputDurationNS > 0 {
		m.AnomalyRatePct = 100 * float64(m.AnomalyDurationNS) / float64(m.InputDurationNS)
	}
	if m.PlannedClipNS > 0 {
		diff := clock.AbsNS(m.ActualClipNS - m.PlannedClipNS)
		m.BudgetDeviationPct = 100 * float64(diff) / float64(m.PlannedClipNS)
	}
	return m
}

func decideExit(cfg Config, conclusions []index.FileConclusion, m Metrics) int {
	for _, c := range conclusions {
		if c.Status == "failed" {
			return 1
		}
	}
	if m.AnomalyRatePct > cfg.AnomalyRateThresholdPct ||
		m.BudgetDeviationPct > cfg.BudgetDeviationThresholdPct {
		return 2
	}
	return 0
}
