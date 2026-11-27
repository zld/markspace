package segment

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"continuity/media"
)

const stateFile = "state.json"

// State records what a run produced: the input hashes, the plan hash
// and the hash of every completed segment. Only segments whose recorded
// hash still matches the artifact on disk are reused after an
// interruption; anything else is recomputed.
type State struct {
	InputHashes []string          `json:"input_hashes"`
	PlanHash    string            `json:"plan_hash"`
	Segments    map[string]string `json:"segments"` // "r<rep>/s<idx>" -> sha256
}

// NewState creates a fresh state for the given inputs and plan.
func NewState(inputs []*media.Container, planHash string) *State {
	s := &State{PlanHash: planHash, Segments: map[string]string{}}
	for _, in := range inputs {
		s.InputHashes = append(s.InputHashes, in.FileSHA256)
	}
	return s
}

// Matches reports whether the state belongs to these inputs and plan.
func (s *State) Matches(inputs []*media.Container, planHash string) bool {
	if s.PlanHash != planHash || len(s.InputHashes) != len(inputs) {
		return false
	}
	for i, in := range inputs {
		if s.InputHashes[i] != in.FileSHA256 {
			return false
		}
	}
	return true
}

// LoadState reads the state file from dir; a missing file yields an
// empty state.
func LoadState(dir string) (*State, error) {
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return &State{Segments: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		// A corrupt state file must not poison the run: start fresh.
		return &State{Segments: map[string]string{}}, nil
	}
	if s.Segments == nil {
		s.Segments = map[string]string{}
	}
	return &s, nil
}

// Save persists the state into dir.
func (s *State) Save(dir string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFile), raw, 0o644)
}

func stateKey(seg *Segment) string {
	return fmt.Sprintf("r%d/s%d", seg.Rep, seg.Index)
}
