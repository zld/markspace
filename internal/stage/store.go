// Package stage persists resumable processing-stage state. Each stage records
// summaries of its inputs and parameters plus its completion status; a stage
// is only reused after an interruption when every summary still matches.
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/model"
)

// StateFile is the on-disk record of one pipeline run.
const StateFile = "stage-state.json"

// Status values for a stage.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Stage is one resumable unit of work.
type Stage struct {
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	InputSummary string            `json:"input_summary"`
	ParamSummary string            `json:"param_summary"`
	Outputs      []string          `json:"outputs,omitempty"`
	OutputSHA    map[string]string `json:"output_sha,omitempty"`
	CPUNanos     int64             `json:"cpu_nanos,omitempty"`
	BytesUsed    int64             `json:"bytes_used,omitempty"`
	Attempts     int               `json:"attempts"`
}

// State is the full persisted run state.
type State struct {
	RunID      string            `json:"run_id"`
	Stages     map[string]*Stage `json:"stages"`
	Order      []string          `json:"order"`
	Interrupt  string            `json:"interrupt_after,omitempty"`
	AlgVersion string            `json:"alg_version"`
}

// Store reads and writes the JSON state file inside a run directory.
type Store struct {
	Dir string
}

// NewStore opens (or creates) the store rooted at dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) path() string { return filepath.Join(s.Dir, StateFile) }

// Load reads persisted state. A missing file yields an empty state, not an
// error; malformed content yields a located ERR_STATE_CORRUPT.
func (s *Store) Load() (*State, error) {
	raw, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return &State{Stages: map[string]*Stage{}}, nil
	}
	if err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "stage", 0, err.Error())
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, model.ErrStateCorrupt.At("", 0, "stage", 0,
			"cannot parse "+StateFile+": "+err.Error())
	}
	if st.Stages == nil {
		st.Stages = map[string]*Stage{}
	}
	return &st, nil
}

// Save atomically replaces the state file (write temp + rename).
func (s *Store) Save(st *State) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return model.ErrStateCorrupt.At("", 0, "stage", 0, err.Error())
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return model.ErrStateCorrupt.At("", 0, "stage", 0, err.Error())
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return model.ErrStateCorrupt.At("", 0, "stage", 0, err.Error())
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return model.ErrStateCorrupt.At("", 0, "stage", 0, err.Error())
	}
	return nil
}

// Summarize returns a stable SHA-256 hex digest over a labeled set of inputs.
// Map keys are sorted so equivalent inputs always hash identically.
func Summarize(label string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "label=%s\n", label)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, fields[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// SHA256File returns the hex SHA-256 of a file.
func SHA256File(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", model.ErrStateCorrupt.At("", 0, "stage", 0, err.Error())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
