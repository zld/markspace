package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Stage string

const (
	StageFingerprint Stage = "fingerprint"
	StageParse       Stage = "parse"
	StageMetadata    Stage = "metadata"
	StageValidate    Stage = "validate"
	StageRecover     Stage = "recover"
	StageManifest    Stage = "manifest"
	StageDone        Stage = "done"
)

// Stages is the deterministic state-machine order.
var Stages = []Stage{
	StageFingerprint,
	StageParse,
	StageMetadata,
	StageValidate,
	StageRecover,
	StageManifest,
	StageDone,
}

func NextStage(s Stage) Stage {
	for i, st := range Stages {
		if st == s && i+1 < len(Stages) {
			return Stages[i+1]
		}
	}
	return StageDone
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// State is the recoverable per-task state machine.
type State struct {
	TaskKey        string   `json:"task_key"`
	Status         Status   `json:"status"`
	CompletedStage Stage    `json:"completed_stage"`
	Inputs         []string `json:"inputs,omitempty"`
	Attempt        int      `json:"attempt"`
}

type Store struct {
	dir string
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(taskKey string) string {
	return filepath.Join(s.dir, taskKey+".state.json")
}

func (s *Store) Load(taskKey string) (*State, bool, error) {
	b, err := os.ReadFile(s.path(taskKey))
	if errors.Is(err, os.ErrNotExist) {
		return &State{TaskKey: taskKey, Status: StatusPending}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, false, err
	}
	return &st, true, nil
}

func (s *Store) Save(st *State) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(st.TaskKey) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(st.TaskKey))
}

func (s *Store) MarkRunning(taskKey string, inputs []string) (*State, error) {
	st, _, err := s.Load(taskKey)
	if err != nil {
		return nil, err
	}
	st.Status = StatusRunning
	st.Inputs = inputs
	st.Attempt++
	return st, s.Save(st)
}

func (s *Store) CompleteStage(taskKey string, stage Stage) (*State, error) {
	st, _, err := s.Load(taskKey)
	if err != nil {
		return nil, err
	}
	st.CompletedStage = stage
	if stage == StageDone {
		st.Status = StatusCompleted
	} else {
		st.Status = StatusRunning
	}
	if err := s.Save(st); err != nil {
		return nil, err
	}
	return st, nil
}
