// Package recover persists run progress so an interrupted execution can
// resume. Checkpoints are only ever written at complete packet boundaries:
// a packet is either fully committed to the output and the checkpoint, or
// not at all. A checkpoint is bound to the input digest; a different input
// is refused, never silently reprocessed.
package recover

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"archive/internal/model"
)

// FileName is the checkpoint file name inside a working directory.
const FileName = "checkpoint.json"

// Checkpoint is the resumable state of one pipeline run.
type Checkpoint struct {
	InputSHA256  string       `json:"inputSHA256"`
	InputSize    int64        `json:"inputSize"`
	Committed    int          `json:"committed"` // packets written to the output
	Recovered    int          `json:"recovered"`
	Skipped      int          `json:"skipped"`
	Lost         int          `json:"lost"`
	MaxDevNs     int64        `json:"maxDevNs"`
	AVViolations int          `json:"avViolations"`
	HashState    []byte       `json:"hashState"` // serialised SHA-256 state
	Done         bool         `json:"done"`
	DamagedInput bool         `json:"damagedInput"`
	FinalSHA256  string       `json:"finalSHA256,omitempty"`
	Errs         model.Errors `json:"errs,omitempty"`
}

// Path returns the checkpoint path inside dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Save writes the checkpoint atomically (tmp file + rename), so a crash
// mid-write never leaves a half-written checkpoint behind.
func Save(dir string, cp *Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, FileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path(dir))
}

// Load reads the checkpoint from dir. os.ErrNotExist is returned when no
// checkpoint exists yet.
func Load(dir string) (*Checkpoint, error) {
	data, err := os.ReadFile(Path(dir))
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint damaged: %w", err)
	}
	if cp.InputSHA256 == "" {
		return nil, errors.New("checkpoint missing input digest")
	}
	return &cp, nil
}

// VerifyInput refuses to resume when the input no longer matches the
// checkpointed digest.
func (cp *Checkpoint) VerifyInput(digest string, size int64) error {
	if cp.InputSHA256 != digest || cp.InputSize != size {
		return fmt.Errorf("%s: checkpoint bound to %s/%d bytes, input is %s/%d bytes",
			model.ErrCheckpointMismatch, cp.InputSHA256[:12], cp.InputSize, digest[:12], size)
	}
	return nil
}
