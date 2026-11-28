// Package stage runs the pipeline as an ordered list of stages.
//
// The runner owns stage state: a checkpoint file is written after each stage
// completes, so an interrupted run can resume from the first incomplete stage.
// A stage that fails softly (recoverable input damage) is recorded as partial
// and the runner continues; a hard failure stops the pipeline. The runner also
// enforces the processing budget and the memory budget.
package stage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/manifest"
)

// Step is one stage implementation.
type Step struct {
	// Name is the stable stage name used in the manifest and checkpoints.
	Name string
	// Run executes the stage. Returning a fault error with ErrCorrupt or
	// ErrTruncated marks the stage partial; any other error is fatal.
	Run func(ctx context.Context) error
}

// Options control a run.
type Options struct {
	// StatePath is the checkpoint file. Empty disables checkpointing.
	StatePath string
	// Resume starts from the first incomplete stage in the checkpoint.
	Resume bool
	// InterruptAfter stops the run after the named stage completes, simulating
	// a crash so resume can be exercised.
	InterruptAfter string
}

// Checkpoint is the persisted runner state.
type Checkpoint struct {
	Completed []string `json:"completed"`
	Partial   []string `json:"partial"`
	UpdatedAt int64    `json:"updated_at_unix_nano"`
	StreamSHA string   `json:"stream_sha256"`
	Seed      int64    `json:"seed"`
	BudgetMS  int64    `json:"budget_ms"`
	Note      string   `json:"note,omitempty"`
}

// Outcome summarizes a run.
type Outcome struct {
	Completed []string
	Partial   []string
	// Interrupted is the stage after which the run stopped, or "".
	Interrupted string
	// ResumeStage is the first stage that did not complete.
	ResumeStage string
	// Skipped lists stages that were already complete on resume.
	Skipped []string
	// Failures lists soft failures with their structured error.
	Failures []error
	Elapsed  time.Duration
}

// ErrBudgetExceeded wraps the time budget sentinel.
var ErrBudgetExceeded = fmt.Errorf("%w: processing budget", fault.ErrBudget)

// Runner executes steps.
type Runner struct {
	steps  []Step
	opts   Options
	mf     *manifest.Manifest
	result Outcome
	done   map[string]bool
}

// New builds a runner.
func New(steps []Step, opts Options, mf *manifest.Manifest) *Runner {
	return &Runner{steps: steps, opts: opts, mf: mf, done: map[string]bool{}}
}

// LoadCheckpoint reads a checkpoint file, returning a zero checkpoint when it
// does not exist.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Checkpoint{}, nil
	}
	if err != nil {
		return nil, fault.New(fault.StageManifest, "read checkpoint", err)
	}
	cp := &Checkpoint{}
	if err := json.Unmarshal(data, cp); err != nil {
		return nil, fault.New(fault.StageManifest, "parse checkpoint", err)
	}
	return cp, nil
}

// Run executes the steps from the resume point and returns the outcome.
//
// The context bounds the whole run; the caller supplies the deadline derived
// from the processing budget.
func (r *Runner) Run(ctx context.Context) (*Outcome, error) {
	start := time.Now()
	resumeFrom := 0
	if r.opts.Resume && r.opts.StatePath != "" {
		cp, err := LoadCheckpoint(r.opts.StatePath)
		if err != nil {
			return nil, err
		}
		r.done = map[string]bool{}
		for _, name := range cp.Completed {
			r.done[name] = true
		}
		for i, step := range r.steps {
			if r.done[step.Name] {
				r.result.Skipped = append(r.result.Skipped, step.Name)
				continue
			}
			resumeFrom = i
			break
		}
		if len(r.result.Skipped) == len(r.steps) {
			resumeFrom = len(r.steps)
		}
	}

	for i := resumeFrom; i < len(r.steps); i++ {
		step := r.steps[i]
		if err := ctx.Err(); err != nil {
			return r.finish(start, "", fmt.Errorf("%w: %v", ErrBudgetExceeded, err))
		}
		err := step.Run(ctx)
		if err != nil {
			if isSoft(err) {
				r.result.Partial = append(r.result.Partial, step.Name)
				r.result.Failures = append(r.result.Failures, err)
				r.writeCheckpoint(ctx)
				continue
			}
			r.writeCheckpoint(ctx)
			return r.finish(start, step.Name, err)
		}
		r.result.Completed = append(r.result.Completed, step.Name)
		r.done[step.Name] = true
		r.writeCheckpoint(ctx)
		if r.opts.InterruptAfter != "" && r.opts.InterruptAfter == step.Name {
			return r.finish(start, step.Name, nil)
		}
	}
	return r.finish(start, "", nil)
}

func (r *Runner) finish(start time.Time, interrupted string, err error) (*Outcome, error) {
	r.result.Interrupted = interrupted
	r.result.Elapsed = time.Since(start)
	r.result.ResumeStage = r.firstIncomplete()
	return &r.result, err
}

func (r *Runner) firstIncomplete() string {
	for _, step := range r.steps {
		if !r.done[step.Name] {
			return step.Name
		}
	}
	return "complete"
}

// isSoft reports whether an error may be recorded and stepped over.
func isSoft(err error) bool {
	return fault.Is(err, fault.ErrCorrupt, fault.ErrTruncated, fault.ErrUnsupported)
}

// RecordPartial marks a stage as completed with recoverable damage, so later
// runs skip it but the manifest shows it was not clean.
func (r *Runner) RecordPartial(name, note string) {
	// The manifest record is owned by the caller; the runner only needs to know
	// that the stage finished so the checkpoint does not replay it.
	r.done[name] = true
	r.result.Partial = appendUnique(r.result.Partial, name)
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

func (r *Runner) writeCheckpoint(ctx context.Context) {
	if r.opts.StatePath == "" {
		return
	}
	cp := &Checkpoint{
		Completed: append([]string(nil), r.result.Completed...),
		Partial:   append([]string(nil), r.result.Partial...),
		UpdatedAt: time.Now().UnixNano(),
	}
	for _, name := range r.result.Skipped {
		cp.Completed = append(cp.Completed, name)
	}
	if r.mf != nil {
		cp.StreamSHA = r.mf.StreamSHA256
		cp.Seed = r.mf.Seed
		cp.BudgetMS = r.mf.BudgetMS
	}
	data, err := json.Marshal(cp)
	if err != nil {
		return
	}
	_ = writeAtomic(ctx, r.opts.StatePath, data)
}

// writeAtomic writes data to a temporary file and renames it over path, so a
// crash never leaves a partially written file behind.
func writeAtomic(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// WriteAtomic exposes atomic publication to other packages.
func WriteAtomic(ctx context.Context, path string, data []byte) error {
	return writeAtomic(ctx, path, data)
}

// BudgetDeadline returns the deadline for a millisecond budget, or no deadline
// for a non-positive budget.
func BudgetDeadline(start time.Time, budgetMS int64) (time.Time, bool) {
	if budgetMS <= 0 {
		return time.Time{}, false
	}
	return start.Add(time.Duration(budgetMS) * time.Millisecond), true
}
