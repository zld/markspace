package stream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Checkpoint is the resumable pipeline state.
type Checkpoint struct {
	Version          int              `json:"version"`
	InputFingerprint string           `json:"input_fingerprint"`
	ProcessedEvents  int              `json:"processed_events"`
	StageOffsets     map[string]int64 `json:"stage_offsets,omitempty"`
	Completed        bool             `json:"completed"`
	IndexPath        string           `json:"index_path,omitempty"`
	Digest           string           `json:"digest,omitempty"`
}

// FingerprintFile returns the SHA-256 of a manifest or checkpoint input.
func FingerprintFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// LoadCheckpoint reads a checkpoint. A missing file returns nil, nil.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// SaveJSONAtomic writes v to a temp file in the destination directory and
// renames it, so readers never observe a partially written target.
func SaveJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// SaveCheckpoint atomically persists cp.
func SaveCheckpoint(path string, cp *Checkpoint) error {
	if cp.Version == 0 {
		cp.Version = 1
	}
	return SaveJSONAtomic(path, cp)
}

// MatchesInput reports whether a checkpoint belongs to fingerprint fp.
func (cp *Checkpoint) MatchesInput(fp string) bool {
	return cp != nil && cp.InputFingerprint == fp
}
