package pipeline

import (
	"context"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/index"
	"github.com/zld/continuity/internal/resume"
)

// ResumeOutcome reports how a continuation run used prior checkpoint state.
type ResumeOutcome struct {
	Index         *index.Index
	Metrics       Metrics
	State         *resume.State
	ResumedBlocks int
}

// RunWithResume validates any existing checkpoint state, then runs the bounded
// pipeline while skipping blocks that still verify. Changed inputs or damaged
// checkpoints are rejected up front with ErrResumeMismatch before any write.
// On a fresh directory the state reports zero resumed blocks and the run
// behaves exactly like Run.
func RunWithResume(ctx context.Context, in *demux.File, policy clock.Policy,
	dir string, fp index.Fingerprint, cfg Config) (*ResumeOutcome, error) {
	state, err := resume.Load(dir, fp)
	if err != nil {
		return nil, err
	}
	if err := state.EnsureContiguous(); err != nil {
		return nil, err
	}

	runCfg := cfg
	if state.Resumed() {
		runCfg.Skip = state.ShouldSkip
	}
	ix, metrics, err := Run(ctx, in, policy, state.Store(), runCfg)
	if err != nil {
		return nil, err
	}
	return &ResumeOutcome{
		Index:         ix,
		Metrics:       metrics,
		State:         state,
		ResumedBlocks: state.ResumedBlocks(),
	}, nil
}
