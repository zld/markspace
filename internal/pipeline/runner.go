package pipeline

import (
	"context"
	"os"

	"mediad/internal/model"
)

// StageFunc executes one stage. It writes all outputs into workDir (the fresh
// directory provided by Prepare) and returns an error if the stage cannot
// complete. A stage function must not mutate committed directories.
type StageFunc func(ctx context.Context, st *State, stage string, workDir string) error

// Runner binds stage names to their functions and drives an Engine through the
// ordered sequence with resume support.
type Runner struct {
	engine *Engine
	stages map[string]StageFunc
}

// NewRunner builds a Runner with one function per ordered stage.
func NewRunner(engine *Engine, funcs map[string]StageFunc) *Runner {
	return &Runner{engine: engine, stages: funcs}
}

// Run executes every non-committed stage for a source, resuming from the last
// committed stage. Already-committed stages are verified rather than re-run,
// and their artifacts are never rewritten, so their digests stay identical.
//
// On interruption or error, uncommitted stage directories remain on disk; the
// next Run call cleans them in Prepare and retries the stage.
func (r *Runner) Run(ctx context.Context, sourceSHA, codec string) (*State, error) {
	st, err := r.engine.LoadState(sourceSHA, codec)
	if err != nil {
		return nil, err
	}

	// First, verify every previously committed stage. A changed digest means
	// on-disk corruption and must abort before any new work.
	for _, name := range OrderedStages {
		if st.IsCommitted(name) {
			if err := r.engine.VerifyCommitted(st, name); err != nil {
				return st, err
			}
		}
	}

	// Clean up any interrupted (running but not committed) stage directory
	// before execution starts.
	for _, name := range OrderedStages {
		rec := st.Stages[name]
		if rec != nil && rec.State == StateRunning {
			if err := os.RemoveAll(r.engine.StageDir(sourceSHA, name)); err != nil {
				return st, model.New(model.ErrUncommittedCleanup, codec, -1,
					"cannot remove interrupted stage %s: %v", name, err)
			}
			rec.State = StatePending
			rec.Artifacts = map[string]string{}
			rec.Outputs = nil
			if err := r.engine.saveState(st); err != nil {
				return st, err
			}
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		next := st.NextPending()
		if next == "" {
			return st, nil // all stages committed
		}
		fn, ok := r.stages[next]
		if !ok {
			return st, model.New(model.ErrStageState, codec, -1, "no function registered for stage %q", next)
		}
		workDir, err := r.engine.Prepare(st, next)
		if err != nil {
			return st, err
		}
		if err := fn(ctx, st, next, workDir); err != nil {
			return st, err
		}
		if _, err := r.engine.Commit(st, next); err != nil {
			return st, err
		}
	}
}

// ArtifactPath returns the path to a committed stage artifact.
func (r *Runner) ArtifactPath(st *State, stage, name string) (string, error) {
	rec := st.Stages[stage]
	if rec == nil || rec.State != StateCommitted {
		return "", model.New(model.ErrStageState, st.Codec, -1, "stage %q not committed", stage)
	}
	return r.engine.CommittedDir(st.SourceSHA, stage) + "/" + name, nil
}

// Engine exposes the underlying engine for callers that need direct state
// access (restore, tests).
func (r *Runner) Engine() *Engine { return r.engine }
