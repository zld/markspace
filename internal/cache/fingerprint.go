// Package cache maintains content fingerprints keyed by container path. The
// fingerprint separates file content (SHA-256), container revision and
// metadata revision so the three replacement scenarios produce distinct,
// independently reportable invalidation outcomes.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/internal/model"
)

// State file names inside the workspace.
const (
	DirName        = "cache"
	FingerprintFile = "fingerprints.json"
)

// Fingerprint captures the three version axes for one container path.
type Fingerprint struct {
	Path             string `json:"path"`
	ContentSHA256    string `json:"content_sha256"`
	ContainerVersion uint32 `json:"container_version"`
	MetadataVersion  uint32 `json:"metadata_version"`
}

// Reason explains why a stored entry was invalidated.
type Reason string

const (
	ReasonFresh            Reason = "fresh"
	ReasonContentReplaced  Reason = "content-replaced"
	ReasonContainerChanged Reason = "container-changed"
	ReasonMetadataChanged  Reason = "metadata-changed"
)

// Decision is the cache evaluation result for one container.
type Decision struct {
	Path              string
	Reason            Reason
	ContentCacheValid bool // payload bytes can be reused
	IndexValid        bool // position table can be reused
}

// Counters aggregate the two invalidation classes required by the run summary.
type Counters struct {
	ContentInvalidations int `json:"content_cache_invalidations"`
	IndexInvalidations   int `json:"index_invalidations"`
}

// Store is the persisted fingerprint table.
type Store struct {
	Entries map[string]Fingerprint `json:"entries"`
}

// LoadStore reads the fingerprint table, treating a missing file as empty.
func LoadStore(root string) (*Store, error) {
	path, err := model.SafeJoin(root, filepath.Join(DirName, FingerprintFile))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Store{Entries: map[string]Fingerprint{}}, nil
	}
	if err != nil {
		return nil, model.WrapError(model.StageCache, 0, 0, "reading fingerprint store", err)
	}
	var s Store
	if err = json.Unmarshal(raw, &s); err != nil {
		return nil, model.WrapError(model.StageCache, 0, 0, "fingerprint store is corrupt", err)
	}
	if s.Entries == nil {
		s.Entries = map[string]Fingerprint{}
	}
	return &s, nil
}

// Save atomically persists the fingerprint table.
func (s *Store) Save(root string) error {
	dir, err := model.SafeJoin(root, DirName)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := model.SafeJoin(root, filepath.Join(DirName, FingerprintFile))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return model.AtomicWrite(path, raw, 0o644)
}

// Evaluate compares a freshly observed fingerprint against the stored one and
// records the resulting counters. The content cache survives container- and
// metadata-only changes; the index survives neither, but a content replacement
// is counted once as a content invalidation (its mandatory index rebuild is a
// consequence, not a separate index-invalidation event).
func (s *Store) Evaluate(observed Fingerprint, c *Counters) Decision {
	prev, ok := s.Entries[observed.Path]
	d := Decision{Path: observed.Path}
	switch {
	case !ok:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonFresh, false, false
	case prev.ContentSHA256 != observed.ContentSHA256:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonContentReplaced, false, false
		c.ContentInvalidations++
	case prev.ContainerVersion != observed.ContainerVersion:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonContainerChanged, true, false
		c.IndexInvalidations++
	case prev.MetadataVersion != observed.MetadataVersion:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonMetadataChanged, true, false
		c.IndexInvalidations++
	default:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonFresh, true, true
	}
	s.Entries[observed.Path] = observed
	return d
}

// Compare reports the decision for observed against the stored entry without
// mutating the store. It is used when a pristine baseline was deliberately
// seeded before a mutation: overwriting that baseline here would erase the
// very difference the run must report.
func (s *Store) Compare(observed Fingerprint) Decision {
	prev, ok := s.Entries[observed.Path]
	d := Decision{Path: observed.Path}
	switch {
	case !ok:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonFresh, false, false
	case prev.ContentSHA256 != observed.ContentSHA256:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonContentReplaced, false, false
	case prev.ContainerVersion != observed.ContainerVersion:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonContainerChanged, true, false
	case prev.MetadataVersion != observed.MetadataVersion:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonMetadataChanged, true, false
	default:
		d.Reason, d.ContentCacheValid, d.IndexValid = ReasonFresh, true, true
	}
	return d
}

// Has reports whether an entry exists for path.
func (s *Store) Has(path string) bool {
	_, ok := s.Entries[path]
	return ok
}
