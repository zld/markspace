// Package checkpoint persists per-node processing results so an interrupted
// run can resume without recomputing unchanged work. Writes are atomic (temp
// file plus rename), and every record carries the input and dependency
// digests it was computed from: a mismatch on resume invalidates the record.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"regress/internal/media"
)

// Record is the persisted outcome of one dependency-graph node.
type Record struct {
	// NodeID identifies the graph node this record belongs to.
	NodeID string `json:"node_id"`
	// InputDigest is the content digest of the node's own input asset.
	InputDigest string `json:"input_digest"`
	// DepDigests maps upstream node IDs to the result digests they produced.
	DepDigests map[string]string `json:"dep_digests"`
	// ResultDigest summarizes this node's output; downstream nodes reference it.
	ResultDigest string `json:"result_digest"`
	// Kind tags the producing subsystem (subtitle, rtp, mp4, manifest).
	Kind string `json:"kind"`
	// Payload holds the node's serializable result (manifest text, summaries).
	Payload []byte `json:"payload,omitempty"`
}

// Store is a directory-backed, file-per-node checkpoint database.
type Store struct {
	dir string
}

// Open creates or opens a checkpoint directory under workDir/checkpoints.
func Open(workDir string) (*Store, error) {
	dir := filepath.Join(workDir, "checkpoints")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("checkpoint: cannot create dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// path returns the record file for a node, with the ID sanitized against path
// separators so node IDs can never escape the checkpoint directory.
func (s *Store) path(nodeID string) string {
	sum := sha256.Sum256([]byte(nodeID))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// Save atomically writes a record. The file is fully fsynced and renamed into
// place, so a crash can never leave a half-written checkpoint.
func (s *Store) Save(rec Record) error {
	if rec.NodeID == "" {
		return media.InvalidInput("checkpoint node id must not be empty")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}
	final := s.path(rec.NodeID)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("checkpoint: open temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint: close: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint: rename: %w", err)
	}
	return nil
}

// Load reads a record. A missing record returns (nil, nil), not an error.
func (s *Store) Load(nodeID string) (*Record, error) {
	data, err := os.ReadFile(s.path(nodeID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checkpoint: read: %w", err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		// Corrupt checkpoint: treat as absent so the node is recomputed.
		return nil, nil
	}
	return &rec, nil
}

// Valid reports whether a stored record still matches the node's current
// input digest and all of its upstream dependency result digests. Any mismatch
// means the partial result is stale and must be discarded.
func (s *Store) Valid(rec *Record, inputDigest string, depDigests map[string]string) bool {
	if rec == nil || rec.InputDigest != inputDigest {
		return false
	}
	if len(rec.DepDigests) != len(depDigests) {
		return false
	}
	for k, v := range depDigests {
		if rec.DepDigests[k] != v {
			return false
		}
	}
	return true
}

// Discard removes a record that failed validation. Missing files are ignored.
func (s *Store) Discard(nodeID string) error {
	err := os.Remove(s.path(nodeID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// All returns every valid record keyed by node ID, sorted for deterministic
// iteration. Unreadable files are skipped rather than failing the whole resume.
func (s *Store) All() (map[string]*Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: list: %w", err)
	}
	out := map[string]*Record{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil || rec.NodeID == "" {
			continue
		}
		out[rec.NodeID] = &rec
	}
	return out, nil
}

// SortedIDs returns record node IDs in stable lexicographic order.
func SortedIDs(recs map[string]*Record) []string {
	ids := make([]string, 0, len(recs))
	for id := range recs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
