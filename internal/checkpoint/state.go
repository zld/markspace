// Package checkpoint persists pipeline progress so an interrupted run can
// resume without redoing completed work. The checkpoint advances only to
// segments that were fully written and digest-verified, and the state file
// itself carries a checksum so a corrupted or replaced checkpoint is
// rejected rather than trusted.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/local/tracecheck/internal/clock"
)

const stateFileName = "checkpoint.json"

// SegmentRecord marks one fully completed and verified segment.
type SegmentRecord struct {
	Index    int    `json:"index"`
	StartNS  int64  `json:"start_ns"`
	EndNS    int64  `json:"end_ns"`
	Digest   string `json:"digest"` // sha256 of the segment content
	CacheKey string `json:"cache_key"`
}

// State is the serializable resume state.
type State struct {
	Version  int             `json:"version"`
	InputID  string          `json:"input_id"`
	Segments []SegmentRecord `json:"segments"`
	// ManifestDigest is set only when the whole run completed successfully.
	ManifestDigest string `json:"manifest_digest,omitempty"`
	// Checksum covers every field above and is verified on load.
	Checksum string `json:"checksum"`
}

// Store loads and atomically saves the resume state inside dir.
type Store struct {
	dir  string
	path string
}

// OpenStore creates the resume directory and returns a store.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, path: filepath.Join(dir, stateFileName)}, nil
}

// Load reads and verifies the checkpoint. A missing file yields an empty
// state for the given input with no error, so a first run starts cleanly.
func (s *Store) Load(inputID string) (*State, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &State{Version: 1, InputID: inputID}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, clock.NewError(clock.CodeCheckpoint, 0, s.path,
			"checkpoint is not valid JSON: "+err.Error())
	}
	want, err := computeChecksum(&st)
	if err != nil {
		return nil, err
	}
	if st.Checksum != want {
		return nil, clock.NewError(clock.CodeCheckpoint, 0, s.path,
			"checkpoint checksum mismatch; refusing to trust replaced state")
	}
	if st.InputID != inputID {
		return nil, clock.NewError(clock.CodeCheckpoint, 0, s.path,
			"checkpoint belongs to a different input: "+st.InputID)
	}
	return &st, nil
}

// Advance appends one completed, verified segment. It only accepts the next
// contiguous index, so the checkpoint can never skip an unfinished segment.
// The state is persisted atomically (temp file + rename) before returning.
func (s *Store) Advance(st *State, rec SegmentRecord) error {
	next := len(st.Segments)
	if rec.Index != next {
		return clock.NewError(clock.CodeCheckpoint, 0, s.path,
			"non-contiguous checkpoint advance")
	}
	st.Segments = append(st.Segments, rec)
	return s.save(st)
}

// Finalize records the completed manifest digest and persists the state.
func (s *Store) Finalize(st *State, manifestDigest string) error {
	st.ManifestDigest = manifestDigest
	return s.save(st)
}

// NextIndex returns the index of the first not-yet-completed segment.
func (s *Store) NextIndex(st *State) int { return len(st.Segments) }

// CompletedDigest returns the stored digest of a completed segment, or "".
func (s *Store) CompletedDigest(st *State, index int) string {
	if index < 0 || index >= len(st.Segments) {
		return ""
	}
	return st.Segments[index].Digest
}

func (s *Store) save(st *State) error {
	sum, err := computeChecksum(st)
	if err != nil {
		return err
	}
	st.Checksum = sum
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// computeChecksum hashes the canonical state with the checksum field blank,
// so a state's integrity is verifiable independently of the stored value.
func computeChecksum(st *State) (string, error) {
	cp := *st
	cp.Checksum = ""
	raw, err := json.Marshal(&cp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
