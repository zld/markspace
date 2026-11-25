package jobs

import (
	"encoding/json"

	"github.com/zld/media-check/internal/fault"
)

// Checkpoint is the durable resume point. State is an opaque, deterministic
// JSON-serializable pipeline snapshot.
type Checkpoint struct {
	TaskKey          string          `json:"task_key"`
	InputFingerprint string          `json:"input_fingerprint"`
	LastStage        string          `json:"last_stage"`
	CompletedStages  []string        `json:"completed_stages"`
	State            json.RawMessage `json:"state,omitempty"`
}

func (s *Store) SaveCheckpoint(cp Checkpoint) error {
	if cp.TaskKey == "" || cp.LastStage == "" {
		return fault.E(fault.StageJobs, fault.CodeInvalidArgument, "checkpoint task key and stage are required")
	}
	if cp.CompletedStages == nil {
		cp.CompletedStages = []string{cp.LastStage}
	}
	return writeJSONAtomic(s.checkpointPath(cp.TaskKey), cp)
}

func (s *Store) LoadCheckpoint(taskKey string) (*Checkpoint, error) {
	var cp Checkpoint
	if err := readJSON(s.checkpointPath(taskKey), &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// ResumeCheckpoint loads a checkpoint only when the exact input fingerprint
// matches. Changed inputs must never reuse old stage state.
func (s *Store) ResumeCheckpoint(taskKey, inputFingerprint string) (*Checkpoint, error) {
	cp, err := s.LoadCheckpoint(taskKey)
	if err != nil {
		return nil, err
	}
	if cp.InputFingerprint != inputFingerprint {
		return nil, fault.E(fault.StageJobs, fault.CodeCheckpointMismatch,
			"input fingerprint differs from checkpoint; refusing to resume: current="+
				inputFingerprint+" checkpoint="+cp.InputFingerprint,
			fault.WithInput(taskKey, inputFingerprint))
	}
	return cp, nil
}
