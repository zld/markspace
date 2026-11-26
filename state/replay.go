package state

import (
	"context"
	"os"

	"archivecheck/checkpoint"
	"archivecheck/internal"
	"archivecheck/pipe"
)

// State is the reconstructed processing state.
type State struct {
	Found             bool                     `json:"found"`
	Phase             string                   `json:"phase"`
	Checkpoint        checkpoint.Checkpoint    `json:"checkpoint"`
	Stages            []checkpoint.StageRecord `json:"-"`
	MaxCursor         int64                    `json:"max_cursor"`
	Resumed           int                      `json:"resumed"`
	PeakInFlightBytes int64                    `json:"replay_peak_bytes"`
}

// Replay loads the atomic checkpoint and stage journal. Journal reads are
// charged to the shared memory budget so replay cannot exceed the CLI limit.
func Replay(ctx context.Context, dir string, budget *pipe.Budget) (*State, error) {
	j, err := checkpoint.Open(dir)
	if err != nil {
		return nil, err
	}
	cp, found, err := j.Load(ctx)
	if err != nil {
		return nil, err
	}
	if budget == nil {
		budget = pipe.NewBudget(0)
	}

	size := int64(1)
	if st, err := os.Stat(j.JournalPath()); err == nil {
		size = st.Size()
		if size <= 0 {
			size = 1
		}
	} else if !os.IsNotExist(err) {
		return nil, internal.WrapFault("state.replay", j.JournalPath(), 0, err)
	}
	if err := budget.Acquire(ctx, size); err != nil {
		return nil, internal.WrapFault("state.replay", j.JournalPath(), 0, err)
	}
	defer budget.Release(size)

	stages, err := j.ReadStages(ctx)
	if err != nil {
		return nil, err
	}

	st := &State{Found: found, Checkpoint: cp, Stages: stages, Phase: checkpoint.StageIngested}
	if found {
		st.Phase = cp.Stage
		st.MaxCursor = cp.Cursor
	}
	for _, rec := range stages {
		if rec.Cursor > st.MaxCursor {
			st.MaxCursor = rec.Cursor
		}
		if rec.Stage == checkpoint.StageCached && rec.Cursor > cp.Cursor {
			st.Resumed++
		}
		if !found && rec.Stage != "" {
			st.Phase = rec.Stage
		}
	}
	st.PeakInFlightBytes = budget.PeakBytes()
	return st, nil
}
