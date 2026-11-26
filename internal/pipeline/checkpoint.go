// Package pipeline orchestrates the scan, plan, cut and commit stages.
// Every stage is atomically checkpointed together with the input, event
// and configuration digests; a restart resumes from the last complete
// stage and never re-runs verified work.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"replay/internal/clock"
)

// Stage names are fixed so checkpoints written by an older run are
// recognizable by a newer one.
const (
	StageScan   = "scan"
	StagePlan   = "plan"
	StageCut    = "cut"
	StageCommit = "commit"
)

var stageOrder = map[string]int{
	StageScan: 1, StagePlan: 2, StageCut: 3, StageCommit: 4,
}

// Checkpoint is the durable record of pipeline progress.
type Checkpoint struct {
	Version      int    `json:"version"`
	Stage        string `json:"stage"`
	InputSHA     string `json:"input_sha256"`
	EventsSHA    string `json:"events_sha256"`
	ConfigSHA    string `json:"config_sha256"`
	InputSize    int64  `json:"input_size"`
	UpdatedAt    string `json:"updated_at"` // deterministic content only
	PlannedCount int    `json:"planned_count"`
	CutCount     int    `json:"cut_count"`
}

// Store persists one checkpoint per work directory.
type Store struct {
	dir string
}

// NewStore creates the checkpoint directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, "checkpoint.json") }

// Load reads the checkpoint; a missing file yields (nil, nil).
func (s *Store) Load() (*Checkpoint, error) {
	raw, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, clock.NewError(clock.KindCheckpoint, "checkpoint",
			"corrupt checkpoint: "+err.Error())
	}
	return &cp, nil
}

// Save atomically replaces the checkpoint file.
func (s *Store) Save(cp *Checkpoint) error {
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".cp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, s.path())
}

// Clear removes a checkpoint (used when bindings no longer match).
func (s *Store) Clear() error {
	err := os.Remove(s.path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Matches verifies that a checkpoint belongs to exactly the inputs and
// configuration of the current run; any changed binding invalidates it.
func (cp *Checkpoint) Matches(inputSHA, eventsSHA, configSHA string, inputSize int64) error {
	switch {
	case cp.InputSHA != inputSHA:
		return clock.NewError(clock.KindCheckpoint, "checkpoint", "input digest mismatch")
	case cp.EventsSHA != eventsSHA:
		return clock.NewError(clock.KindCheckpoint, "checkpoint", "events digest mismatch")
	case cp.ConfigSHA != configSHA:
		return clock.NewError(clock.KindCheckpoint, "checkpoint", "config digest mismatch")
	case cp.InputSize != inputSize:
		return clock.NewError(clock.KindCheckpoint, "checkpoint", "input size mismatch")
	}
	return nil
}

// Reached reports whether the named stage has already completed.
func (cp *Checkpoint) Reached(stage string) bool {
	return stageOrder[cp.Stage] >= stageOrder[stage]
}

// Advance validates stage ordering before persisting progress.
func (s *Store) Advance(cp *Checkpoint, stage string) error {
	if stageOrder[stage] != stageOrder[cp.Stage]+1 {
		return clock.NewError(clock.KindCheckpoint, "advance",
			"stage progression violated: "+cp.Stage+" -> "+stage)
	}
	cp.Stage = stage
	return s.Save(cp)
}

// hashFile returns the SHA-256 of a file streamed in bounded chunks.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}
