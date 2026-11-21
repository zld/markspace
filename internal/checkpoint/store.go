// Package checkpoint persists pipeline state atomically and decides which
// completed segments may be reused on resume.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bitrack/internal/model"
)

const (
	// FileName is the canonical checkpoint inside the work directory.
	FileName = "checkpoint.json"
	// tmpSuffix keeps the in-progress file distinct before atomic rename.
	tmpSuffix = ".tmp"
)

// Store reads and writes checkpoints inside one work directory.
type Store struct{ dir string }

// NewStore creates a store rooted at dir; the directory must already exist.
func NewStore(dir string) (*Store, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("workdir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workdir %s is not a directory", dir)
	}
	return &Store{dir: dir}, nil
}

// Path returns the checkpoint file location.
func (s *Store) Path() string { return filepath.Join(s.dir, FileName) }

// Save writes cp to a temp file, fsyncs it, then atomically renames it over
// the previous checkpoint. A crash at any point leaves the old file intact.
func (s *Store) Save(cp *model.Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	final := s.Path()
	tmp := final + tmpSuffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create checkpoint temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync checkpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close checkpoint: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("atomic rename checkpoint: %w", err)
	}
	return nil
}

// Load reads and parses the checkpoint. A missing file returns os.IsNotExist.
func (s *Store) Load() (*model.Checkpoint, error) {
	data, err := os.ReadFile(s.Path())
	if err != nil {
		return nil, err
	}
	var cp model.Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint %s (corrupt state): %w", s.Path(), err)
	}
	return &cp, nil
}

// FileSHA256 returns the lowercase hex SHA-256 checksum of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestParams produces a stable fingerprint of the encoding parameters so
// that any parameter change invalidates previously committed segments.
func DigestParams(p model.Params) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("digest params: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// DigestBaseline hashes the bitrate-independent encoding subset. A run at a
// different target kbps with the same encoder settings matches the same
// baseline, while any encoder/geometry change does not.
func DigestBaseline(p model.Params) (string, error) {
	data, err := json.Marshal(p.BaselineKey())
	if err != nil {
		return "", fmt.Errorf("digest baseline key: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ReuseDecision explains why a committed segment was or was not reused.
type ReuseDecision struct {
	Reusable bool
	Reason   string
}

// VerifyCommitted checks one committed segment against the current run:
// fingerprint/params match is checked by the caller; here we verify the output
// file exists, is non-empty and its checksum still matches the checkpoint.
func VerifyCommitted(seg *model.Segment) ReuseDecision {
	if seg.State != model.StateCommitted {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d state is %s, not committed", seg.Index, seg.State)}
	}
	if seg.OutputPath == "" {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d has no output path", seg.Index)}
	}
	info, err := os.Stat(seg.OutputPath)
	if err != nil {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d output missing: %v", seg.Index, err)}
	}
	if info.Size() == 0 {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d output is empty (truncated file)", seg.Index)}
	}
	got, err := FileSHA256(seg.OutputPath)
	if err != nil {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d checksum error: %v", seg.Index, err)}
	}
	if seg.Checksum == "" {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d checkpoint has no checksum", seg.Index)}
	}
	if got != seg.Checksum {
		return ReuseDecision{Reason: fmt.Sprintf("segment %d checksum mismatch: file %s != recorded %s", seg.Index, got, seg.Checksum)}
	}
	return ReuseDecision{Reusable: true}
}
