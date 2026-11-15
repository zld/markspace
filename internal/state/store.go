// Package state persists task checkpoints atomically and recovers them on
// restart. A checkpoint is one self-describing JSON document written via
// temp-file-and-rename so readers never observe a half-written file.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"transcode/internal/model"
	"transcode/internal/safeio"
)

// SchemaVersion is bumped whenever the on-disk layout changes incompatibly.
const SchemaVersion = 1

// ErrCorruptCheckpoint means the file exists but cannot be parsed or fails
// schema validation; callers map this to exit code 4.
var ErrCorruptCheckpoint = errors.New("checkpoint corrupt or incompatible")

// Checkpoint is the full persisted state of one job.
type Checkpoint struct {
	SchemaVersion    int              `json:"schema_version"`
	JobID            string           `json:"job_id"`
	Params           Params           `json:"params"`
	InputFingerprint string           `json:"input_fingerprint"`
	Segments         []*model.Segment `json:"segments"`
}

// Params is the parameter summary embedded in every checkpoint. Two runs
// with identical Params plus identical input fingerprint are resumable.
type Params struct {
	InputPath      string  `json:"input_path"`
	DurationSec    float64 `json:"duration_sec"`
	SegmentSec     float64 `json:"segment_seconds"`
	TargetKbps     float64 `json:"target_kbps"`
	MinKbps        float64 `json:"min_kbps"`
	EncoderCommand string  `json:"encoder_command"`
	Workers        int     `json:"workers"`
	MaxOutputBytes int64   `json:"max_output_bytes"`
}

// Store reads and writes checkpoint files under a fixed directory root.
type Store struct {
	root string
}

// NewStore validates that path stays inside root and prepares the directory.
func NewStore(root, path string) (*Store, string, error) {
	abs, err := safeio.ResolveUnder(root, path)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, "", err
	}
	return &Store{root: root}, abs, nil
}

// Save serializes the checkpoint and atomically replaces the destination.
func (s *Store) Save(path string, cp *Checkpoint) error {
	return s.SaveLocked(path, cp)
}

// SaveLocked performs the write without any store-level serialization;
// callers that already hold their own lock use this to avoid
// lock-ordering surprises.
func (s *Store) SaveLocked(path string, cp *Checkpoint) error {
	cp.SchemaVersion = SchemaVersion
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	dir := filepath.Dir(path)
	f, tmp, err := safeio.TempFile(dir, ".cp-", ".tmp")
	if err != nil {
		return err
	}
	cleanup := func() {
		f.Close()
		os.Remove(tmp)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Load reads and validates a checkpoint. A missing file returns
// (nil, nil) so callers can distinguish "first run" from corruption.
func (s *Store) Load(path string) (*Checkpoint, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptCheckpoint, err)
	}
	if err := cp.validate(); err != nil {
		return nil, err
	}
	return &cp, nil
}

func (cp *Checkpoint) validate() error {
	if cp.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema version %d, want %d",
			ErrCorruptCheckpoint, cp.SchemaVersion, SchemaVersion)
	}
	if cp.JobID == "" || len(cp.Segments) == 0 {
		return fmt.Errorf("%w: missing job id or segments", ErrCorruptCheckpoint)
	}
	valid := map[model.Status]bool{
		model.StatusQueued: true, model.StatusRunning: true,
		model.StatusSucceeded: true, model.StatusFailed: true,
		model.StatusInterrupted: true,
	}
	for i, seg := range cp.Segments {
		if seg == nil || seg.Index != i || !valid[seg.Status] {
			return fmt.Errorf("%w: segment %d invalid", ErrCorruptCheckpoint, i)
		}
	}
	return nil
}
