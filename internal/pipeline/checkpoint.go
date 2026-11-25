package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"tracecheck/internal/clock"
)

// CheckpointFile is the on-disk checkpoint name inside the output directory.
const CheckpointFile = "checkpoint.json"

// Checkpoint is the durable resume record.
type Checkpoint struct {
	InputDigest     string         `json:"input_digest"`
	CompletedStages []Stage        `json:"completed_stages"`
	CurrentStage    Stage          `json:"current_stage"`
	LastSafeOffset  clock.Offset   `json:"last_safe_offset"`
	Transitions     int            `json:"transitions"`
}

// Digest computes the hex SHA-256 of input bytes.
func Digest(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

// snapshot writes the checkpoint without touching any completed stage record.
func (s *State) snapshot(inputDigest string, lastSafe clock.Offset) Checkpoint {
	done := make([]Stage, 0, len(s.finished))
	for _, stage := range AllStages() {
		if s.finished[stage] {
			done = append(done, stage)
		}
	}
	return Checkpoint{
		InputDigest:     inputDigest,
		CompletedStages: done,
		CurrentStage:    s.current,
		LastSafeOffset:  lastSafe,
		Transitions:     s.transitions,
	}
}

// Save writes the checkpoint atomically (temp file + rename) so a crash cannot
// leave a half-written record.
func Save(dir string, cp Checkpoint) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(dir, CheckpointFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Load reads a checkpoint and refuses reuse when the input digest differs.
func Load(dir, inputDigest string) (*Checkpoint, error) {
	raw, err := os.ReadFile(filepath.Join(dir, CheckpointFile))
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, err
	}
	if cp.InputDigest != inputDigest {
		return nil, &clock.ErrDigestMismatch{
			Expected: cp.InputDigest, Actual: inputDigest,
		}
	}
	return &cp, nil
}

// Restore rebuilds an in-memory State from a validated checkpoint.
func Restore(cp *Checkpoint) *State {
	s := NewState()
	for _, stage := range cp.CompletedStages {
		s.finished[stage] = true
	}
	s.current = cp.CurrentStage
	s.transitions = cp.Transitions
	return s
}
