// Package job tracks pipeline phase transitions and their input/output
// digests, and persists enough state to resume from the last successful phase
// without touching sealed source assets.
package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"replaycheck/internal/model"
)

// Record is the digest trail for one phase.
type Record struct {
	Stage      model.Stage `json:"stage"`
	InputSum   string      `json:"inputDigest"`
	OutputSum  string      `json:"outputDigest"`
	Passes     int         `json:"passes"`
	Cached     bool        `json:"cached"`
	ResumeFrom model.Stage `json:"resumeFrom,omitempty"`
}

// State is the persisted pipeline state.
type State struct {
	Version    int         `json:"version"`
	Fixture    string      `json:"fixture"`
	SourceSum  string      `json:"sourceDigest"`
	EventSum   string      `json:"eventDigest"`
	LastOK     model.Stage `json:"lastOkStage"`
	FailedAt   model.Stage `json:"failedStage,omitempty"`
	Failure    string      `json:"failure,omitempty"`
	FailureOff uint64      `json:"failureOffset"`
	Records    []Record    `json:"records"`
	Complete   bool        `json:"complete"`
}

// ErrNoState means there is nothing to resume from.
var ErrNoState = errors.New("no prior job state")

// Store persists State as a single JSON document.
type Store struct {
	Path string
}

func NewStore(dir string) *Store { return &Store{Path: filepath.Join(dir, "state.json")} }

// Load reads the persisted state. A missing file is ErrNoState, not an error
// the caller has to string-match.
func (s *Store) Load() (*State, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoState
	}
	if err != nil {
		return nil, model.WrapError(model.StageIngest, "state", model.TrackRef{}, 0, "cannot read job state", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, model.WrapError(model.StageIngest, "state", model.TrackRef{}, 0, "corrupt job state", err)
	}
	if st.Version != 1 {
		return nil, model.Errorf(model.StageIngest, "state", model.TrackRef{}, 0, "unsupported state version %d", st.Version)
	}
	return &st, nil
}

// Save writes state atomically.
func (s *Store) Save(st *State) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return model.WrapError(model.StageIngest, "state", model.TrackRef{}, 0, "cannot create state dir", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return model.WrapError(model.StageIngest, "state", model.TrackRef{}, 0, "cannot encode state", err)
	}
	tmp := s.Path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return model.WrapError(model.StageIngest, "state", model.TrackRef{}, 0, "cannot write state", err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		os.Remove(tmp)
		return model.WrapError(model.StageIngest, "state", model.TrackRef{}, 0, "cannot publish state", err)
	}
	return nil
}

// NewState starts a fresh job for a source/event pair.
func NewState(fixture, sourceSum, eventSum string) *State {
	return &State{Version: 1, Fixture: fixture, SourceSum: sourceSum, EventSum: eventSum}
}
