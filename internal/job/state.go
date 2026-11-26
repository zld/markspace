// Package job persists resumable pipeline phase state.
package job

import (
	"encoding/json"
	"os"
	"path/filepath"

	"medpipe/internal/apperr"
	"medpipe/internal/store"
)

const (
	StatusNew       = "new"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusPaused    = "paused_budget"
	StatusIsolated  = "isolated"
	StatusFailed    = "failed"
)

const (
	StagePending   = "pending"
	StageRunning   = "running"
	StagePrepared  = "prepared"
	StageCommitted = "committed"
	StageSkipped   = "skipped"
)

// StageState is the write-ahead/commit record for one phase.
type StageState struct {
	Name             string `json:"name"`
	Status           string `json:"status"`
	Artifact         string `json:"artifact,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Size             int64  `json:"size,omitempty"`
	FirstSample      int    `json:"first_sample,omitempty"`
	LastSample       int    `json:"last_sample,omitempty"`
	CheckpointSample int    `json:"checkpoint_sample,omitempty"`
	ProcessedThrough int    `json:"processed_through,omitempty"`
	Attempts         int    `json:"attempts,omitempty"`
	Message          string `json:"message,omitempty"`
}

// ArtifactState records a published output and its content hash.
type ArtifactState struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// BudgetState holds estimate/actual accounting.
type BudgetState struct {
	Samples       int  `json:"samples"`
	Repairs       int  `json:"repairs"`
	EstimateUnits int  `json:"estimate_units"`
	ActualUnits   int  `json:"actual_units"`
	BudgetUnits   int  `json:"budget_units"`
	Paused        bool `json:"paused"`
}

// State is the durable job record.
type State struct {
	JobID        string                   `json:"job_id"`
	JobKey       string                   `json:"job_key"`
	ManifestPath string                   `json:"manifest_path,omitempty"`
	ManifestHash string                   `json:"manifest_sha256"`
	Status       string                   `json:"status"`
	Fault        string                   `json:"fault,omitempty"`
	Budget       BudgetState              `json:"budget"`
	Stages       map[string]*StageState   `json:"stages"`
	Artifacts    map[string]ArtifactState `json:"artifacts,omitempty"`
}

// Dir returns the job state/output directory.
func Dir(root, jobID string) string { return filepath.Join(root, jobID) }

// StatePath returns state.json under the job directory.
func StatePath(root, jobID string) string {
	return filepath.Join(Dir(root, jobID), "state.json")
}

// Load reads state.  A missing state returns a new state and false.
func Load(root, jobID string) (*State, bool, error) {
	path := StatePath(root, jobID)
	data, err := os.ReadFile(path)
	if store.IsNotExist(err) {
		return &State{
			JobID:     jobID,
			Status:    StatusNew,
			Stages:    map[string]*StageState{},
			Artifacts: map[string]ArtifactState{},
		}, false, nil
	}
	if err != nil {
		return nil, false, apperr.New("E_JOB_STATE", "read state: %v", err).WithStage("state")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, apperr.New("E_JOB_STATE", "parse state: %v", err).WithStage("state")
	}
	if s.Stages == nil {
		s.Stages = map[string]*StageState{}
	}
	if s.Artifacts == nil {
		s.Artifacts = map[string]ArtifactState{}
	}
	return &s, true, nil
}

// Save atomically persists state.json.
func (s *State) Save(root, jobID string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return apperr.New("E_JOB_STATE", "encode state: %v", err).WithStage("state")
	}
	path := StatePath(root, jobID)
	if _, err := store.WriteAtomic(path, append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// Stage returns an initialized stage record.
func (s *State) Stage(name string) *StageState {
	st, ok := s.Stages[name]
	if !ok || st == nil {
		st = &StageState{Name: name, Status: StagePending}
		s.Stages[name] = st
	}
	return st
}

// IsCommitted reports whether a phase result is durable.
func (s *State) IsCommitted(name string) bool {
	st, ok := s.Stages[name]
	return ok && st != nil && st.Status == StageCommitted
}

// PutArtifact records a verified published artifact.
func (s *State) PutArtifact(name, path, sha string, size int64) {
	s.Artifacts[name] = ArtifactState{Path: path, SHA256: sha, Size: size}
}

// NearestSync returns the latest sync sample not after processedThrough.
// Sync samples are one-based.  Sample 1 is always an implicit fallback.
func NearestSync(syncSamples []int, processedThrough int) int {
	best := 1
	for _, n := range syncSamples {
		if n <= processedThrough && n > best {
			best = n
		}
	}
	return best
}

// ResumeSampleRange returns the sync cut and inclusive sample range to reprocess.
func ResumeSampleRange(syncSamples []int, processedThrough, total int) (checkpoint int, first int, last int) {
	checkpoint = NearestSync(syncSamples, processedThrough)
	if checkpoint > total {
		checkpoint = NearestSync(syncSamples, total)
	}
	return checkpoint, checkpoint, total
}
