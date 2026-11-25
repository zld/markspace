// Package app wires the scan, clock, align, stream, manifest, provenance and
// checkpoint subsystems into the end-to-end media pipeline and exposes the
// operations the terminal adapter needs: demo runs, interrupted/resumable
// runs, repeatability checks and standalone manifest verification.
package app

import (
	"context"
	"os"
	"path/filepath"

	"mediapipeline/internal/align"
	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/provenance"
	"mediapipeline/internal/scan"
)

// Default file locations under the output directory.
const (
	ManifestPath    = "manifest.json"
	TracePath       = "provenance.json"
	CheckpointPath  = "pipeline.checkpoint"
	ManifestVersion = "v1"
)

// Config is the resolved run configuration from CLI parameters.
type Config struct {
	InputDir        string
	OutputDir       string
	Case            fixture.Case
	Representations int
	Fragments       int
	QueueCapacity   int
	// InterruptAfter stops the pipeline after this stage without advancing
	// past it (empty means run to completion).
	InterruptAfter checkpoint.Stage
	Resume         bool
}

// Summary is the deterministic terminal-facing result of a run.
type Summary struct {
	State             string                 `json:"state"`
	Representations   int                    `json:"representations"`
	AlignedSegments   int                    `json:"aligned_segments"`
	CommittedSegments int                    `json:"committed_segments"`
	QueuePeak         int                    `json:"queue_capacity_peak"`
	QueueCapacity     int                    `json:"queue_capacity"`
	GapCount          int                    `json:"gap_count"`
	RegressionCount   int                    `json:"regression_count"`
	ManifestValid     bool                   `json:"manifest_valid"`
	ManifestSHA256    string                 `json:"manifest_sha256,omitempty"`
	ProvenanceSHA256  string                 `json:"provenance_sha256,omitempty"`
	ProvenanceDiff    int                    `json:"provenance_diff"`
	CheckpointVersion int                    `json:"checkpoint_version"`
	CheckpointStage   string                 `json:"checkpoint_stage"`
	ErrorCode         string                 `json:"error,omitempty"`
	ErrorInput        string                 `json:"error_input,omitempty"`
	RecoveryHint      string                 `json:"recovery_hint,omitempty"`
	SafeResumeStage   string                 `json:"safe_resume_stage,omitempty"`
	ConflictDiffs     []provenance.FieldDiff `json:"conflict_diffs,omitempty"`
}

// repState holds one representation's working data.
type repState struct {
	desc     fixture.RepresentationDescriptor
	init     *scan.InitInfo
	tracker  *scan.SequenceTracker
	boundary *scan.RTPBoundary
	frags    []*scan.Fragment
}

// Pipeline owns the directories and checkpoint store for one run.
type Pipeline struct {
	cfg       Config
	store     *checkpoint.Store
	descs     []fixture.RepresentationDescriptor
	states    []*repState
	alignment *align.Report
	peak      int
}

// New validates config and prepares output paths.
func New(cfg Config) (*Pipeline, error) {
	if cfg.Representations < 2 {
		return nil, fault.New(fault.CodeInvalidArgument, "--representations must be >= 2")
	}
	if cfg.QueueCapacity < 1 {
		return nil, fault.New(fault.CodeInvalidArgument, "--queue must be >= 1")
	}
	if cfg.Fragments == 0 {
		cfg.Fragments = 5
	}
	if cfg.Case == "" {
		cfg.Case = fixture.CaseNormal
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, err
	}
	return &Pipeline{
		cfg:   cfg,
		store: checkpoint.NewStore(filepath.Join(cfg.OutputDir, CheckpointPath)),
	}, nil
}

// Generate (re)creates the deterministic input samples.
func (p *Pipeline) Generate(ctx context.Context) error {
	genCfg := fixture.Config{
		Root:            p.cfg.InputDir,
		Case:            p.cfg.Case,
		Representations: p.cfg.Representations,
		Fragments:       p.cfg.Fragments,
	}
	descs, err := fixture.Generate(genCfg)
	if err != nil {
		return err
	}
	p.descs = descs
	p.states = make([]*repState, len(descs))
	for i, d := range descs {
		p.states[i] = &repState{desc: d}
	}
	return nil
}

// Run executes the full stage flow and returns a summary. A returned error
// is always a *fault.Error; the summary is still populated for reporting.
func (p *Pipeline) Run(ctx context.Context) (*Summary, error) {
	summary := &Summary{
		State:           string(checkpoint.StageScan),
		Representations: p.cfg.Representations,
		QueueCapacity:   p.cfg.QueueCapacity,
	}
	if len(p.descs) == 0 {
		// On resume the recorded fault case must drive regeneration so the
		// recreated inputs match the digests stored in the checkpoint.
		if cp := p.existingCheckpoint(); cp != nil && cp.Case != "" {
			p.cfg.Case = fixture.Case(cp.Case)
		}
		if err := p.Generate(ctx); err != nil {
			return summary, err
		}
	}

	if err := p.stageScan(ctx, summary); err != nil {
		return summary, err
	}
	if p.interruptAt(checkpoint.StageScan, summary) {
		return summary, nil
	}
	if err := p.stageAlign(ctx, summary); err != nil {
		return summary, err
	}
	if p.interruptAt(checkpoint.StageAlign, summary) {
		return summary, nil
	}
	if err := p.stageManifest(ctx, summary); err != nil {
		return summary, err
	}
	if p.interruptAt(checkpoint.StageManifest, summary) {
		return summary, nil
	}
	if err := p.stageComplete(ctx, summary); err != nil {
		return summary, err
	}
	summary.State = string(checkpoint.StageComplete)
	return summary, nil
}

func (p *Pipeline) interruptAt(stage checkpoint.Stage, summary *Summary) bool {
	if p.cfg.InterruptAfter != stage {
		return false
	}
	summary.State = "interrupted:" + string(stage)
	summary.SafeResumeStage = string(stage)
	return true
}

// scanItem is the work unit scheduled through the bounded queue.
type scanItem struct {
	repIdx  int
	path    string
	relPath string
	index   int
}
