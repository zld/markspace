package index

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"mediapack/internal/fault"
)

// Key is the rebuild identity: source content, encapsulation parameters, and
// target platform.
type Key struct {
	SourceSHA256 string `json:"source_sha256"`
	ParamsSHA256 string `json:"params_sha256"`
	Platform     string `json:"platform"`
}

// Spec describes an asset that could be reused or rebuilt.
type Spec struct {
	ID  string `json:"id"`
	Key Key    `json:"key"`
}

// Record is the persisted last-known-good index entry.
type Record struct {
	ID             string            `json:"id"`
	Key            Key               `json:"key"`
	Outputs        map[string]string `json:"outputs"`
	SegmentHashes  map[string]string `json:"segment_hashes,omitempty"`
	ManifestSHA256 string            `json:"manifest_sha256,omitempty"`
}

// Store is the JSON content-addressed rebuild index.
type Store struct {
	SchemaVersion int               `json:"schema_version"`
	Assets        map[string]Record `json:"assets"`
	path          string
}

// Plan explains the decision for one asset.
type Plan struct {
	Reused  []Decision `json:"reused_assets"`
	Rebuilt []Decision `json:"rebuilt_assets"`
}

// Decision records why an asset was reused or rebuilt.
type Decision struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Key    Key    `json:"key"`
}

// LoadOrCreate reads a store or returns an empty one at path.
func LoadOrCreate(path string) (*Store, error) {
	s := &Store{SchemaVersion: 1, Assets: map[string]Record{}, path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fault.Wrap(fault.Checkpoint, err, "read index %s", path)
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fault.Wrap(fault.DigestMismatch, err, "parse index %s", path)
	}
	if s.Assets == nil {
		s.Assets = map[string]Record{}
	}
	s.path = path
	return s, nil
}

// Save atomically persists the index.
func (s *Store) Save() error {
	b, err := CanonicalJSON(s)
	if err != nil {
		return err
	}
	if err := AtomicWriteFile(s.path, b, 0o644); err != nil {
		return fault.Wrap(fault.Checkpoint, err, "save index")
	}
	return nil
}

// AtomicWriteFile writes via a same-directory temp file and rename.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Plan classifies specs. Only an asset whose complete Key is unchanged and has
// a persisted record is reused.
func (s *Store) Plan(specs []Spec) Plan {
	p := Plan{}
	for _, spec := range specs {
		old, ok := s.Assets[spec.ID]
		switch {
		case !ok:
			p.Rebuilt = append(p.Rebuilt, Decision{spec.ID, "new_asset", spec.Key})
		case old.Key.SourceSHA256 != spec.Key.SourceSHA256:
			p.Rebuilt = append(p.Rebuilt, Decision{spec.ID, "source_digest_changed", spec.Key})
		case old.Key.ParamsSHA256 != spec.Key.ParamsSHA256:
			p.Rebuilt = append(p.Rebuilt, Decision{spec.ID, "params_digest_changed", spec.Key})
		case old.Key.Platform != spec.Key.Platform:
			p.Rebuilt = append(p.Rebuilt, Decision{spec.ID, "platform_changed", spec.Key})
		default:
			p.Reused = append(p.Reused, Decision{spec.ID, "digests_match", spec.Key})
		}
	}
	return p
}

// Commit records a successfully built asset.
func (s *Store) Commit(rec Record) {
	if rec.Outputs == nil {
		rec.Outputs = map[string]string{}
	}
	s.Assets[rec.ID] = rec
}

// SummarySHA256 hashes the stable IDs and reasons in a plan.
func (p Plan) SummarySHA256() string {
	type compact struct {
		Reused  []Decision `json:"reused"`
		Rebuilt []Decision `json:"rebuilt"`
	}
	b, _ := CanonicalJSON(compact(p))
	return SHA256Hex(b)
}
