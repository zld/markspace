// Package pipeline orchestrates parse, transform, validate and commit as
// four independently checkpointed stages. A failed stage never publishes a
// half-built index: the index file is only renamed into place at commit, and
// recovery resumes from the highest verified checkpoint.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"medfix/internal/index"
	"medfix/internal/journal"
	"medfix/internal/timeline"
)

// Input is one self-generated source stream.
type Input struct {
	ID        string
	Kind      string // "mp4" or "rtp"
	Data      []byte
	ClockRate uint32 // required for RTP inputs
}

// Options configures a run.
type Options struct {
	WorkDir   string
	Config    timeline.Config
	Interrupt string // stage after which to simulate a process exit
	Fault     *journal.Fault
}

// Run executes (or resumes) one pipeline execution.
type Run struct {
	RunID        string
	store        journal.CheckpointStore
	inputs       []Input
	cfg          timeline.Config
	inputSHA     string
	configHash   string
	resumedStage string
	indexPath    string
	provPath     string
	opts         Options

	parseLosses []timeline.Error
}

// Report is the machine-readable result of a run.
type Report struct {
	RunID              string           `json:"run_id"`
	ResumedStage       string           `json:"resumed_stage"`
	Segments           int              `json:"segments"`
	SegmentSHA256      []string         `json:"segment_sha256"`
	RoundtripMatchRate float64          `json:"roundtrip_match_rate"`
	KeyframeHitRate    float64          `json:"keyframe_hit_rate"`
	InvariantFailures  int              `json:"invariant_failures"`
	SourceOffsets      []int64          `json:"source_offset"`
	Conflicts          []timeline.Error `json:"conflicts"`
	NoKeyframeTracks   []string         `json:"no_keyframe_tracks"`
	IndexSHA256        string           `json:"index_sha256"`
}

// New prepares a run. The input fingerprint hashes all inputs in order.
func New(runID string, inputs []Input, opts Options) (*Run, error) {
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, err
	}
	baseStore, err := journal.NewStore(filepath.Join(opts.WorkDir, "journal"))
	if err != nil {
		return nil, err
	}
	var store journal.CheckpointStore = baseStore
	if opts.Fault != nil {
		store = journal.NewFaultyStore(baseStore, opts.Fault)
	}
	cfgJSON, _ := json.Marshal(opts.Config)
	h := sha256.New()
	for _, in := range inputs {
		h.Write(in.Data)
	}
	r := &Run{
		RunID: runID, store: store,
		inputs: inputs, cfg: opts.Config,
		inputSHA:   hex.EncodeToString(h.Sum(nil)),
		configHash: journal.ConfigHash(cfgJSON),
		indexPath:  filepath.Join(opts.WorkDir, "index.mfix"),
		provPath:   filepath.Join(opts.WorkDir, "provenance.json"),
		opts:       opts,
	}
	return r, nil
}

// Execute drives the four stages with checkpoint-guarded resume. Only
// uncommitted stages are redone; repairs are never applied twice because
// transform always starts from freshly re-parsed events.
func (r *Run) Execute() (*Report, error) {
	cp, idx, err := r.store.LatestValid(r.inputSHA, r.configHash)
	if err != nil && !errors.Is(err, journal.ErrCheckpointMissing) &&
		!errors.Is(err, journal.ErrFingerprintMismatch) &&
		!errors.Is(err, journal.ErrConfigMismatch) {
		return nil, err
	}
	startStage := journal.StageParse
	if cp != nil && idx >= 0 {
		startStage = journal.Stages[min(idx+1, len(journal.Stages)-1)]
		r.resumedStage = cp.Stage
	}

	// Parse is deterministic and stream-based; re-parsing on resume rebuilds
	// in-memory state without re-applying any repair.
	aligner, err := r.stageParse()
	if err != nil {
		return nil, err
	}
	if leStage(startStage, journal.StageParse) {
		if err := r.writeCheckpoint(journal.StageParse, 0); err != nil {
			return nil, err
		}
	}

	var result *timeline.AlignmentResult
	if leStage(startStage, journal.StageTransform) {
		if result, err = r.stageTransform(aligner); err != nil {
			return nil, err
		}
		if err := r.writeCheckpoint(journal.StageTransform, 1); err != nil {
			return nil, err
		}
		if r.opts.Interrupt == journal.StageTransform {
			return nil, ErrInterrupted
		}
	} else {
		// Re-derive the repaired timeline from parsed state for reporting.
		if result, err = r.stageTransform(aligner); err != nil {
			return nil, err
		}
	}

	if leStage(startStage, journal.StageValidate) {
		if err := r.stageValidate(result); err != nil {
			return nil, err
		}
		if err := r.writeCheckpoint(journal.StageValidate, 2); err != nil {
			return nil, err
		}
	}

	var built *index.Index
	if leStage(startStage, journal.StageCommit) {
		// If an index file already exists at commit time (resume after a
		// crash between rename and the commit checkpoint), verify it before
		// trusting it. A truncated/corrupt index is a hard failure rather
		// than a silently rebuilt one, so data loss stays visible.
		if loaded, loadErr := index.Load(r.indexPath); loadErr == nil {
			built = loaded
		} else if !errors.Is(loadErr, os.ErrNotExist) {
			return nil, loadErr
		}
		if built == nil {
			if built, err = r.stageCommit(result); err != nil {
				return nil, err
			}
		}
		if err := r.writeCheckpoint(journal.StageCommit, 3); err != nil {
			return nil, err
		}
	} else {
		built, err = index.Load(r.indexPath)
		if err != nil {
			return nil, err
		}
	}
	return r.buildReport(built, result)
}

func (r *Run) writeCheckpoint(stage string, stageIdx int) error {
	prev := ""
	if stageIdx > 0 {
		if pc, err := r.store.Read(journal.Stages[stageIdx-1]); err == nil {
			prev = pc.BodyHash
		}
	}
	cp := &journal.Checkpoint{
		RunID: r.RunID, Stage: stage, StageIndex: stageIdx,
		InputSHA256: r.inputSHA, ConfigHash: r.configHash,
		PrevHash: prev, PayloadPath: r.indexPath,
	}
	return r.store.Write(cp)
}

// ErrInterrupted signals a simulated process exit between stages.
var ErrInterrupted = errors.New("pipeline: simulated interruption")

func leStage(a, b string) bool { return stageRank(a) <= stageRank(b) }

func stageRank(s string) int {
	for i, st := range journal.Stages {
		if st == s {
			return i
		}
	}
	return len(journal.Stages)
}

// InputSHA exposes the fingerprint for tests/demo output.
func (r *Run) InputSHA() string { return r.inputSHA }

// ResumedStage exposes the stage from which a run resumed.
func (r *Run) ResumedStage() string { return r.resumedStage }
