// Package checkpoint persists resumable pipeline progress at complete
// sample boundaries. Files are written atomically, checksummed, and a
// one-generation backup is kept so a corrupt checkpoint can fall back to a
// safe boundary with a recorded reason.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"mediascan/internal/model"
)

// Version is the checkpoint format version.
const Version = 1

// State is the persisted resume state. It contains only deterministic data.
type State struct {
	Version            int                   `json:"version"`
	ManifestKey        string                `json:"manifest_key"`
	CommittedSamples   int64                 `json:"committed_samples"`
	CompletedSegments  int                   `json:"completed_segments"`
	TotalSegments      int                   `json:"total_segments"`
	Complete           bool                  `json:"complete"`
	ResultSHA256       string                `json:"result_sha256,omitempty"`
	Segments           []model.SegmentReport `json:"segments,omitempty"`
}

// Store manages one checkpoint file and its .bak generation.
type Store struct {
	// Path is the effective checkpoint file path.
	Path string
}

// Open resolves path: an existing directory yields path/checkpoint.json.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("checkpoint: empty path")
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		path = filepath.Join(path, "checkpoint.json")
	}
	return &Store{Path: path}, nil
}

type envelope struct {
	Checksum string `json:"checksum"`
	State    *State `json:"state"`
}

// stateChecksum returns the canonical compact encoding and its digest. Both
// writer and reader canonicalize the typed State the same way, so on-disk
// indentation can never invalidate the checksum.
func stateChecksum(st *State) (raw []byte, sum string, err error) {
	raw, err = json.Marshal(st)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(raw)
	return raw, hex.EncodeToString(h[:]), nil
}

// Save atomically persists state with a checksum. The previous file, if any,
// is retained as Path+".bak".
func (s *Store) Save(st *State) error {
	if st == nil {
		return errors.New("checkpoint: nil state")
	}
	st.Version = Version
	_, checksum, err := stateChecksum(st)
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}
	env := envelope{Checksum: checksum, State: st}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal envelope: %w", err)
	}

	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: mkdir: %w", err)
	}

	// Keep the last known-good generation before replacing it.
	if existing, err := os.ReadFile(s.Path); err == nil {
		_ = os.WriteFile(s.Path+".bak", existing, 0o600)
	}

	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("checkpoint: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}

// LoadResult describes what Load recovered.
type LoadResult struct {
	State            *State
	Source           string // main | backup | fresh
	RollbackReason   string
}

// Load reads and verifies the checkpoint, applying the safe fallback rules.
func (s *Store) Load() (*LoadResult, error) {
	mainBytes, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &LoadResult{State: &State{Version: Version}, Source: "fresh"}, nil
		}
		return nil, fmt.Errorf("checkpoint: read: %w", err)
	}

	st, verr := decodeVerified(mainBytes)
	if verr == nil {
		return &LoadResult{State: st, Source: "main"}, nil
	}

	if bakBytes, berr := os.ReadFile(s.Path + ".bak"); berr == nil {
		if bst, bverr := decodeVerified(bakBytes); bverr == nil {
			return &LoadResult{
				State:          bst,
				Source:         "backup",
				RollbackReason: fmt.Sprintf("primary checkpoint corrupt (%v); resumed from previous safe boundary", verr),
			}, nil
		}
	}

	return &LoadResult{
		State:          &State{Version: Version},
		Source:         "fresh",
		RollbackReason: fmt.Sprintf("checkpoint corrupt (%v); rolled back to safe boundary resume_from_sample=0", verr),
	}, nil
}

func decodeVerified(b []byte) (*State, error) {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	if env.State == nil || env.Checksum == "" {
		return nil, errors.New("missing checksum or state")
	}
	_, want, err := stateChecksum(env.State)
	if err != nil {
		return nil, fmt.Errorf("canonicalize state: %w", err)
	}
	if want != env.Checksum {
		return nil, errors.New("checksum mismatch")
	}
	st := env.State
	if st.Version != Version {
		return nil, fmt.Errorf("unsupported version %d", st.Version)
	}
	if st.CommittedSamples < 0 || st.CompletedSegments < 0 {
		return nil, errors.New("negative progress")
	}
	return st, nil
}

// Remove deletes the checkpoint and its backup. Used by cold runs.
func (s *Store) Remove() {
	_ = os.Remove(s.Path)
	_ = os.Remove(s.Path + ".bak")
}
