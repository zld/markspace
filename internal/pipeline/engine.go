// Package pipeline wires the kernel modules into one auditable, resumable
// processing engine.
//
// The engine runs a fixed DAG (parse -> jitter -> recover -> sync -> mux),
// persists every completed node through the checkpoint store, and records a
// provenance entry per node. Recovered samples are materialized as artifacts so
// that, after an interruption, a fresh process can resume from the most recent
// complete node and still produce byte-identical output.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/dag"
	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/provenance"
)

// Node names in the fixed DAG.
const (
	NodeParse   = "parse"
	NodeJitter  = "jitter"
	NodeRecover = "recover"
	NodeSync    = "sync"
	NodeMux     = "mux"
)

// Config controls one engine run.
type Config struct {
	// RunID separates checkpoints of different logical runs.
	RunID string
	// Seed is the fixture generation seed (also recorded in provenance).
	Seed int64
	// WorkDir holds the checkpoint and intermediate artifacts.
	WorkDir string
	// OutPath is the only location the MP4 may be written to.
	OutPath string
	// JitterWindow is the bounded reorder window in sequence positions.
	JitterWindow int
	// SyncToleranceTicks is the maximum allowed absolute A/V offset.
	SyncToleranceTicks uint64
	// VideoDelayTicks shifts video relative to audio on the shared timeline.
	VideoDelayTicks uint64
	// FailNodeOnce, when non-empty, makes that node fail one time (used to
	// simulate an interruption and prove resume correctness).
	FailNodeOnce string
	// StrictRecovery makes any gap fatal instead of recovered.
	StrictRecovery bool
	// AudioSpec optionally injects fixture faults (truncation, corruption,
	// rollover, late packets). When nil a clean stream is generated.
	AudioSpec *fixture.Spec
}

// Report is the verifiable result of a run.
type Report struct {
	Packets          int
	Duplicates       int
	Gaps             int
	SyncOffsetTicks  int64
	ReusedNodes      []string
	BlockedDependency *dag.Blocked
	FirstError       *mpierr.MediaError
	Summary          string // output file digest, empty when mux did not run
	ProvenanceDigest string
}

// Engine owns the shared state of one run.
type Engine struct {
	cfg     Config
	store   *checkpoint.Store
	rec     *provenance.Recorder
	sched   *dag.Scheduler
	attempt map[string]int // node -> attempts in this process

	// Runtime results, populated as nodes complete (also reloaded on resume).
	syncOffset   int64
	outputDigest string
	genMeta      fixture.Meta
}

// GenMeta returns the audio fixture generation metadata (fault offsets etc.).
func (e *Engine) GenMeta() fixture.Meta { return e.genMeta }

// NewEngine builds and validates an engine and its DAG.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.RunID == "" || cfg.WorkDir == "" || cfg.OutPath == "" {
		return nil, fmt.Errorf("pipeline: RunID, WorkDir and OutPath are required")
	}
	if cfg.JitterWindow <= 0 {
		cfg.JitterWindow = 8
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("pipeline: workdir: %w", err)
	}
	graph, err := dag.New([]dag.Node{
		{Name: NodeParse},
		{Name: NodeJitter, Deps: []string{NodeParse}},
		{Name: NodeRecover, Deps: []string{NodeJitter}},
		{Name: NodeSync, Deps: []string{NodeRecover}},
		{Name: NodeMux, Deps: []string{NodeSync}},
	})
	if err != nil {
		return nil, err
	}
	store, err := checkpoint.Open(filepath.Join(cfg.WorkDir, "checkpoint.json"), cfg.RunID)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:     cfg,
		store:   store,
		rec:     provenance.NewRecorder(cfg.RunID, cfg.Seed),
		attempt: map[string]int{},
	}
	funcs := map[string]dag.NodeFunc{
		NodeParse:   e.runParse,
		NodeJitter:  e.runJitter,
		NodeRecover: e.runRecover,
		NodeSync:    e.runSync,
		NodeMux:     e.runMux,
	}
	e.sched = dag.NewScheduler(graph, store, funcs)
	return e, nil
}

// Store exposes the checkpoint store (for inspection and interval records).
func (e *Engine) Store() *checkpoint.Store { return e.store }

// Recorder exposes the provenance recorder.
func (e *Engine) Recorder() *provenance.Recorder { return e.rec }

// markDone persists a node with its idempotency tuple and saves atomically.
func (e *Engine) markDone(node, inputDigest, configDigest string) error {
	if err := e.sched.MarkDone(node, inputDigest, configDigest); err != nil {
		return err
	}
	return e.store.Save()
}

// failIfInterrupted simulates a one-shot node failure for resume testing.
func (e *Engine) failIfInterrupted(node string) error {
	if e.cfg.FailNodeOnce != node {
		return nil
	}
	e.attempt[node]++
	if e.attempt[node] == 1 {
		return mpierr.New(mpierr.CatUnknown, 0, node,
			"simulated interruption before commit")
	}
	return nil
}
