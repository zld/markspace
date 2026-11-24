package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Checkpoint is the persisted record of one completed node.
type Checkpoint struct {
	// Key is the node's idempotency key (digest of inputs, params, version).
	Key string `json:"key"`
	// Node names this result depends on, for provenance and invalidation.
	Dependencies []string `json:"dependencies"`
	// OutputHash is the content summary of the node's produced result.
	OutputHash string `json:"output_hash"`
	// SourceOffsets records the input byte ranges the result was derived from.
	SourceOffsets []SourceOffset `json:"source_offsets"`
	// NodeVersion is the CodeVersion under which the result was produced.
	NodeVersion string `json:"node_version"`
}

// SourceOffset pins a result to a half-open byte range of one input.
type SourceOffset struct {
	Input string `json:"input"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

// Store persists checkpoints as one JSON document per key, written atomically
// so an interrupted run can never leave a half-written record behind.
type Store struct {
	dir string
}

// NewStore opens (or creates) a checkpoint directory.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("pipeline: checkpoint directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("pipeline: cannot create checkpoint dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(key string) string {
	return filepath.Join(s.dir, key+".json")
}

// Save writes the checkpoint atomically: temp file in the same directory,
// fsync, then rename. A crash at any point leaves either the old record or
// the new one, never a partial document.
func (s *Store) Save(cp Checkpoint) error {
	if cp.Key == "" {
		return errors.New("pipeline: checkpoint key must not be empty")
	}
	final := s.path(cp.Key)
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("pipeline: cannot create temp checkpoint: %w", err)
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cp); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("pipeline: cannot encode checkpoint: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("pipeline: cannot fsync checkpoint: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pipeline: cannot finalize checkpoint: %w", err)
	}
	return nil
}

// Load returns the checkpoint for a key and whether it exists.
func (s *Store) Load(key string) (Checkpoint, bool, error) {
	b, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("pipeline: cannot read checkpoint: %w", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return Checkpoint{}, false, fmt.Errorf("pipeline: corrupt checkpoint for %s: %w", key, err)
	}
	return cp, true, nil
}

// Valid reports whether a stored checkpoint is still usable for node. It is
// invalid when the key mismatches (inputs/params/version changed) or the code
// version moved on. Dependency validity is the runner's responsibility.
func (s *Store) Valid(cp Checkpoint, node *Node) bool {
	if cp.NodeVersion != CodeVersion {
		return false
	}
	key, err := node.Key()
	if err != nil {
		return false
	}
	return cp.Key == key
}

// Remove deletes a checkpoint, e.g. when a node must be recomputed.
func (s *Store) Remove(key string) error {
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Keys lists every checkpoint key currently stored, for diagnostics.
func (s *Store) Keys() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".json" {
			keys = append(keys, name[:len(name)-len(".json")])
		}
	}
	return keys, nil
}
