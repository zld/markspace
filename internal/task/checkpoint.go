package task

import (
	"os"
	"path/filepath"

	"mediacore/internal/model"
)

// RunResult reports how a stage's payload was obtained.
type RunResult struct {
	Payload []byte
	Resumed bool // true when loaded from a valid checkpoint
	Redone  bool // true when a corrupt checkpoint forced a redo
}

// HasCheckpoint reports whether any checkpoint file exists for the stage,
// valid or not.
func (s *Store) HasCheckpoint(taskKey string, stage model.Stage) bool {
	dir, err := s.taskDir(taskKey)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, string(stage)+".json"))
	return err == nil
}

// RunStage returns the payload for one pipeline stage. When a valid
// checkpoint exists it is reused; when a checkpoint exists but fails
// verification the stage (and everything after it) is invalidated and
// recomputed. fn must produce the stage payload deterministically.
func (s *Store) RunStage(taskKey string, stage model.Stage, fn func() ([]byte, error)) (RunResult, error) {
	payload, _, err := s.Load(taskKey, stage)
	if err == nil {
		return RunResult{Payload: payload, Resumed: true}, nil
	}
	me, ok := err.(*model.Error)
	if !ok {
		return RunResult{}, err
	}
	redone := false
	if me.Category == model.ErrCheckpoint && s.HasCheckpoint(taskKey, stage) {
		// Present but unverifiable: roll back this stage onward.
		redone = true
		if ierr := s.Invalidate(taskKey, stage); ierr != nil {
			return RunResult{}, ierr
		}
	}
	out, ferr := fn()
	if ferr != nil {
		return RunResult{}, ferr
	}
	if serr := s.Save(taskKey, stage, out); serr != nil {
		return RunResult{}, serr
	}
	return RunResult{Payload: out, Redone: redone}, nil
}
