package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	mediaindex "github.com/zld/mediarc/internal/index"
	"github.com/zld/mediarc/internal/model"
)

// Checkpoint format version.
const CheckpointVersion = 1

// InputSummary is the deterministic identity of the parsed source asset.
type InputSummary struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// RecoveryPoint is a bounded prefix checkpoint that can replay a suffix.
type RecoveryPoint struct {
	Offset    int                    `json:"offset"`
	Watermark WatermarkState         `json:"watermark"`
	Unwrap    map[uint32]UnwrapState `json:"unwrap"`
	Index     mediaindex.State       `json:"index"`
}

// Checkpoint contains everything required to validate and resume a run.
type Checkpoint struct {
	Version int `json:"version"`

	Input  InputSummary `json:"input"`
	Config model.Config `json:"config"`

	ProcessedEvents int `json:"processed_events"`

	Watermark WatermarkState         `json:"watermark"`
	Unwrap    map[uint32]UnwrapState `json:"unwrap"`
	Index     mediaindex.State       `json:"index"`
	Recovery  []RecoveryPoint        `json:"recovery_points,omitempty"`

	// BaseManifestSHA256 is optional but lets the late-event acceptance path
	// prove reproducibility against the completed ordered run.
	BaseManifestSHA256 string `json:"base_manifest_sha256,omitempty"`
}

// SaveCheckpoint writes deterministic indented JSON and atomically renames it.
func SaveCheckpoint(path string, cp *Checkpoint) error {
	if cp == nil {
		return &model.ConfigError{Field: "checkpoint", Err: errors.New("nil checkpoint")}
	}
	cp.Version = CheckpointVersion
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCheckpoint reads and performs structural validation.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, &model.IntegrityError{Path: path, Err: fmt.Errorf("%w: %v", model.ErrCheckpoint, err)}
	}
	if cp.Version != CheckpointVersion {
		return nil, &model.IntegrityError{Path: path, Err: fmt.Errorf("%w: unsupported version %d", model.ErrCheckpoint, cp.Version)}
	}
	if cp.Input.SHA256 == "" {
		return nil, &model.IntegrityError{Path: path, Err: fmt.Errorf("%w: missing input digest", model.ErrCheckpoint)}
	}
	if err := cp.Config.Validate(); err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	return &cp, nil
}

// ResolveInputPath resolves a checkpoint-relative input path without allowing
// an absolute recorded path to override an explicitly adjacent asset.
func (cp *Checkpoint) ResolveInputPath(checkpointPath string) string {
	if filepath.IsAbs(cp.Input.Path) {
		return cp.Input.Path
	}
	return filepath.Join(filepath.Dir(checkpointPath), cp.Input.Path)
}

// VerifyBytes checks the source digest before continuation.
func (cp *Checkpoint) VerifyBytes(data []byte) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != cp.Input.SHA256 {
		return &model.IntegrityError{
			Path: cp.Input.Path,
			Err:  fmt.Errorf("%w: checkpoint=%s actual=%s", model.ErrDigestMismatch, cp.Input.SHA256, got),
		}
	}
	if cp.Input.Size != 0 && cp.Input.Size != int64(len(data)) {
		return &model.IntegrityError{
			Path: cp.Input.Path,
			Err:  fmt.Errorf("%w: size checkpoint=%d actual=%d", model.ErrDigestMismatch, cp.Input.Size, len(data)),
		}
	}
	return nil
}

// VerifyFile reads and verifies the checkpoint's source asset.
func (cp *Checkpoint) VerifyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &model.IntegrityError{Path: path, Err: err}
	}
	if err := cp.VerifyBytes(data); err != nil {
		return nil, err
	}
	return data, nil
}
