package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/timeline"
)

// IndexRecord is the persisted form of a timeline index. It is addressed by
// the source hash, but its validity is the conjunction of source hash,
// metadata hash and parser version.
type IndexRecord struct {
	timeline.Index
}

// indexPath maps a source hash to its index file under root/index.
func (s *Store) indexPath(sourceHash string) string {
	return filepath.Join(s.root, "index", sourceHash[:2], sourceHash+".json")
}

// SaveIndex atomically persists an index for its source hash.
func (s *Store) SaveIndex(idx *timeline.Index) error {
	if idx == nil || idx.SourceHash == "" {
		return errors.New("cache: index missing source hash")
	}
	dst := s.indexPath(idx.SourceHash)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("cache: marshal index: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "idx-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, dst)
}

// LoadIndex reads a persisted index for a source hash. A missing index is not
// an error: it returns (nil, nil) so callers rebuild.
func (s *Store) LoadIndex(sourceHash string) (*timeline.Index, error) {
	data, err := os.ReadFile(s.indexPath(sourceHash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: read index: %w", err)
	}
	var idx timeline.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("cache: parse index: %w", err)
	}
	return &idx, nil
}

// CanReuseDerived reports whether derived segments may be reused. The cache
// key encodes track content + processing parameters, so reuse is allowed when
// the cache key is unchanged even if source/metadata digests differ — this is
// precisely the "metadata-only edit" case.
func CanReuseDerived(cached, current *timeline.Index) bool {
	if cached == nil || current == nil {
		return false
	}
	return cached.CacheKey == current.CacheKey
}

// IndexDigest returns a stable digest of an index, used to prove repeated runs
// produce byte-identical indices and manifests.
func IndexDigest(idx *timeline.Index) (string, error) {
	data, err := json.Marshal(idx)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
