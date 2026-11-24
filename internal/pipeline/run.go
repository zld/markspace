// Package pipeline orchestrates parse, scan, repair, index and verify for one
// demo run. Each stage is checkpointed with input, parameter and stage-version
// digests; a rerun resumes from the last trusted checkpoint and reproduces the
// same output digest.
package pipeline

import (
	"path/filepath"

	"mediad/internal/clock"
	"mediad/internal/fixture"
	"mediad/internal/index"
	"mediad/internal/metrics"
	mp4 "mediad/internal/mp4"
	"mediad/internal/provenance"
)

// Options configures one run.
type Options struct {
	WorkDir   string
	Seed      int64
	TargetBps int64
	Corrupt   string
	Resume    bool
	// ExternalManifest marks a caller-supplied manifest that must never be
	// overwritten by fixture generation.
	ExternalManifest bool
	InferSync        bool
	Regenerate       bool
	ManifestPath     string
	MediaRoot        string
	CheckpointDir    string
	OutDir           string
	Policy           mp4.RepairPolicy
	Version          int
}

// Defaults fills omitted options.
func (o *Options) Defaults() {
	if o.WorkDir == "" {
		o.WorkDir = "demo-work"
	}
	if o.CheckpointDir == "" {
		o.CheckpointDir = filepath.Join(o.WorkDir, "checkpoints")
	}
	if o.OutDir == "" {
		o.OutDir = filepath.Join(o.WorkDir, "out")
	}
	if o.ManifestPath == "" {
		o.ManifestPath = filepath.Join(o.WorkDir, "manifest.json")
	}
	if o.MediaRoot == "" {
		o.MediaRoot = o.WorkDir
	}
	if o.Version == 0 {
		o.Version = 1
	}
	// Missing keyframe flags are recoverable: infer conservative GOP boundaries
	// and label the result derived so it stays auditable.
	if o.Corrupt == fixture.CorruptMissingSync {
		o.InferSync = true
	}
	if o.Policy.MaxGapTicks == 0 {
		o.Policy = mp4.DefaultPolicy()
	}
}

// paramSet is the canonical parameter snapshot hashed into every checkpoint.
type paramSet struct {
	Seed      int64  `json:"seed"`
	TargetBps int64  `json:"target_bps"`
	Corrupt   string `json:"corrupt"`
	InferSync bool   `json:"infer_sync"`
	Version   int    `json:"version"`
	IndexVer  int    `json:"index_version"`
	RepairVer int    `json:"repair_version"`
	// Policy fields affect repaired output, so they belong here.
	MaxGapTicks int64 `json:"max_gap_ticks"`
}

// Result is the deliverable summary.
type Result struct {
	Report      *metrics.Report         `json:"report"`
	Provenance  *provenance.Record      `json:"provenance"`
	Indexes     map[string]*index.Index `json:"-"`
	Resumed     bool                    `json:"resumed"`
	OutputSHA   string                  `json:"output_sha256"`
	InputSHA    string                  `json:"input_sha256"`
	ManifestSHA string                  `json:"manifest_sha256"`
}

// Run executes the pipeline. The full stage orchestration is added in the
// following commits; this skeleton keeps the package importable.
func Run(opts Options) (*Result, error) {
	opts.Defaults()
	return nil, clock.New(clock.StageArgs, clock.CodeBadArgs, "pipeline not yet assembled")
}
