// Package checkpoint persists per-material pipeline progress so an interrupted
// run can resume. Only stages whose products were atomically written and whose
// digests still match are reused; everything else is recomputed.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/example/mediad/internal/model"
)

// StageState records one completed pipeline stage for a material.
type StageState struct {
	Stage        model.Stage `json:"stage"`
	PacketFrom   uint64      `json:"packetFrom"` // inclusive extended sequence
	PacketTo     uint64      `json:"packetTo"`   // inclusive extended sequence
	OutputDigest string      `json:"outputDigest"`
	ProductPath  string      `json:"productPath"` // atomically written artifact
}

// State is the full checkpoint for one material.
type State struct {
	MaterialID     string       `json:"materialId"`
	MaterialDigest string       `json:"materialDigest"`
	Stages         []StageState `json:"stages"`
	ManifestDigest string       `json:"manifestDigest"`
}

// Store reads and writes checkpoint files inside a working directory.
type Store struct {
	dir string
}

// NewStore opens (creating if needed) a checkpoint directory.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(materialID string) string {
	// Hex-digest the id into a filename so it cannot escape the directory.
	sum := sha256.Sum256([]byte(materialID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// Save atomically writes a checkpoint via temp-file-plus-rename.
func (s *Store) Save(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	final := s.path(st.MaterialID)
	tmp, err := os.CreateTemp(s.dir, "cp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, final)
}

// Load reads a checkpoint. A missing file returns (nil, nil) so a first run
// starts clean.
func (s *Store) Load(materialID string) (*State, error) {
	b, err := os.ReadFile(s.path(materialID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Reusable returns the stages that may be safely reused: they must belong to a
// checkpoint whose material digest matches, and each must reference an
// existing, atomically written product whose output digest still matches.
// Stages failing any check are dropped; the caller recomputes them. A
// digest-level disagreement is surfaced as CHECKPOINT_STALE.
func (s *Store) Reusable(materialID, materialDigest string) ([]StageState, bool, error) {
	st, err := s.Load(materialID)
	if err != nil {
		return nil, false, err
	}
	if st == nil {
		return nil, false, nil
	}
	if st.MaterialDigest != materialDigest {
		return nil, false, model.NewError(model.CodeCheckpointStale, model.StageCheckpoint,
			0, -1, "checkpoint: material digest changed")
	}
	var keep []StageState
	for _, ss := range st.Stages {
		if !productIntact(ss) {
			continue
		}
		keep = append(keep, ss)
	}
	return keep, len(keep) > 0, nil
}

// productIntact verifies the artifact exists and its digest matches the record.
func productIntact(ss StageState) bool {
	if ss.ProductPath == "" {
		return false
	}
	b, err := os.ReadFile(ss.ProductPath)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) == ss.OutputDigest
}

// Digest is a convenience SHA-256 hex helper for material content.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
