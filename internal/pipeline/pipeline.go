// Package pipeline is the reusable orchestration kernel. It wires the
// metadata, RTP recovery, alignment, render, report and transaction
// subsystems into ordered, recoverable stages, each guarded by a WAL intent
// and an atomic, hash-verified commit. The CLI is a thin shell over it.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/fixture"
	"mediasync/internal/model"
	"mediasync/internal/render"
	"mediasync/internal/report"
	"mediasync/internal/rtp"
	"mediasync/internal/sync"
	"mediasync/internal/txn"
)

// Config controls a pipeline run. Limits are deliberately explicit so memory,
// wait windows and output size stay bounded.
type Config struct {
	Seed          int64
	WaitWindow    int
	MaxBuffered   int
	MaxDriftMs    int64
	Render        render.Config
	ReorderCfg    rtp.ReorderConfig
	JitterCfg     rtp.JitterConfig
	AlignmentCfg  sync.AlignmentConfig
}

// DefaultConfig returns bounded offline defaults.
func DefaultConfig(seed int64) Config {
	rc := rtp.DefaultRecoveryConfig()
	return Config{
		Seed: seed, WaitWindow: rc.Reorder.WaitWindow, MaxBuffered: rc.Reorder.MaxBuffered,
		MaxDriftMs:   40,
		Render:       render.DefaultConfig(),
		ReorderCfg:   rc.Reorder,
		JitterCfg:    rc.Jitter,
		AlignmentCfg: sync.DefaultAlignmentConfig(),
	}
}

// StageResult is the verified outcome of one committed stage.
type StageResult struct {
	Stage    string        `json:"stage"`
	Manifest txn.Manifest  `json:"manifest"`
}

// Result is the full pipeline outcome handed to the CLI and verify.
type Result struct {
	OutDir      string             `json:"out_dir"`
	Stages      []StageResult      `json:"stages"`
	Summary     *report.Summary    `json:"summary"`
	SummaryHash string             `json:"summary_hash"`
	Records     []model.ErrorRecord `json:"records"`
	FailedStage string             `json:"failed_stage,omitempty"`
	Failure     *model.ErrorRecord `json:"failure,omitempty"`
}

// Pipeline owns one run's working directory and transaction manager.
type Pipeline struct {
	cfg          Config
	mgr          *txn.Manager
	stageResults []StageResult
}

// New opens a pipeline rooted at outDir.
func New(cfg Config, outDir string) (*Pipeline, error) {
	mgr, err := txn.NewManager(outDir)
	if err != nil {
		return nil, err
	}
	return &Pipeline{cfg: cfg, mgr: mgr}, nil
}

// Close releases the transaction manager.
func (p *Pipeline) Close() error { return p.mgr.Close() }

// Manager exposes the transaction manager for recovery/verification.
func (p *Pipeline) Manager() *txn.Manager { return p.mgr }

// Run executes every stage transactionally. On failure it rolls back the
// open stage, records the fault and returns a Result whose FailedStage is
// set; the caller maps that to exit code 1.
func (p *Pipeline) Run(fx *fixture.Fixture) (*Result, error) {
	res := &Result{OutDir: p.mgr.RootDir()}
	rec := model.NewRecorder()

	// Stage 1: metadata adjudication.
	mxfData := fx.MXF
	if fx.Fault == fixture.FaultTruncated {
		mxfData = fx.TruncatedMXF
	}
	if stageErr := p.stage(txn.StageMetadata, rec, func() ([]artifact, error) {
		return p.runMetadata(mxfData, fx.MP4, rec)
	}); stageErr != nil {
		return p.fail(res, txn.StageMetadata, rec, stageErr)
	}

	// Stage 2: RTP recovery.
	var rstats rtp.RecoveryStats
	if stageErr := p.stage(txn.StageRecovery, rec, func() ([]artifact, error) {
		var arts []artifact
		_, rstats, arts = p.runRecovery(fx, rec)
		return arts, nil
	}); stageErr != nil {
		return p.fail(res, txn.StageRecovery, rec, stageErr)
	}

	// Stage 3: alignment.
	var aligned *sync.Result
	if stageErr := p.stage(txn.StageAlign, rec, func() ([]artifact, error) {
		var arts []artifact
		aligned, arts = p.runAlign(fx, rec)
		return arts, nil
	}); stageErr != nil {
		return p.fail(res, txn.StageAlign, rec, stageErr)
	}

	// Stage 4: offscreen render.
	var frames []render.Frame
	var audio []render.AudioFragment
	if stageErr := p.stage(txn.StageRender, rec, func() ([]artifact, error) {
		var arts []artifact
		frames, audio, arts = p.runRender(aligned, rec)
		return arts, nil
	}); stageErr != nil {
		return p.fail(res, txn.StageRender, rec, stageErr)
	}

	// Stage 5: quantitative report.
	summary := report.Build(p.cfg.Seed, rstats, aligned, frames, audio)
	if stageErr := p.stage(txn.StageReport, rec, func() ([]artifact, error) {
		return p.runReport(summary)
	}); stageErr != nil {
		return p.fail(res, txn.StageReport, rec, stageErr)
	}

	hash, err := summary.Hash()
	if err != nil {
		return nil, err
	}
	res.Summary = summary
	res.SummaryHash = hash
	res.Stages = p.StageManifests()
	res.Records = rec.Records()
	return res, nil
}

// fail finalizes a failed run: captures the located fault for the CLI.
func (p *Pipeline) fail(res *Result, stage string, rec *model.Recorder, cause error) (*Result, error) {
	res.FailedStage = stage
	res.Records = rec.Records()
	if rec.HasError() {
		last := lastError(rec.Records())
		res.Failure = &last
	}
	return res, cause
}

func lastError(records []model.ErrorRecord) model.ErrorRecord {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Severity == model.SevError {
			return records[i]
		}
	}
	if len(records) > 0 {
		return records[len(records)-1]
	}
	return model.ErrorRecord{Code: model.ErrTxnIncomplete, Message: "stage failed without a recorded error"}
}

// artifact is an in-memory file handed to a stage's transaction.
type artifact struct {
	name string
	data []byte
}

// stage runs one WAL-guarded unit: begin, write artifacts, commit; on any
// error it rolls back so no half-finished directory survives.
func (p *Pipeline) stage(name string, rec *model.Recorder, work func() ([]artifact, error)) error {
	if err := p.mgr.Begin(name); err != nil {
		return err
	}
	arts, err := work()
	if err != nil {
		_ = p.mgr.Rollback(name)
		return err
	}
	if rec.HasError() {
		_ = p.mgr.Rollback(name)
		return fmt.Errorf("pipeline: stage %s reported errors", name)
	}
	for _, a := range arts {
		if _, err := p.mgr.WriteStaging(name, a.name, a.data); err != nil {
			_ = p.mgr.Rollback(name)
			return err
		}
	}
	man, err := p.mgr.Commit(name)
	if err != nil {
		_ = p.mgr.Rollback(name)
		return err
	}
	p.stageResults = append(p.stageResults, StageResult{Stage: name, Manifest: *man})
	return nil
}

// StageManifests returns a copy of the committed manifests in run order.
func (p *Pipeline) StageManifests() []StageResult {
	out := make([]StageResult, len(p.stageResults))
	copy(out, p.stageResults)
	return out
}

// ensureDir is a small helper for stages that need an on-disk scratch space.
func ensureDir(path string) error { return os.MkdirAll(path, 0o755) }

var _ = filepath.Separator
