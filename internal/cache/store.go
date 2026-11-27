package cache

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediakernel/internal/clock"
	"mediakernel/internal/fsx"
)

// Entry is the on-disk verification record accompanying one cached blob.
type Entry struct {
	SourceID       string `json:"source_id"`
	InputDigest    string `json:"input_digest"`
	IndexDigest    string `json:"index_digest"`
	ArtifactDigest string `json:"artifact_digest"`
	Key            string `json:"key"`
}

// Store is a directory-backed content-addressed cache rooted inside an
// explicitly supplied sandbox base.
type Store struct {
	base string
	dir  string
}

// NewStore opens (creating) a cache directory under base.
func NewStore(base, relDir string) (*Store, error) {
	dir, err := fsx.EnsureOutput(base, filepath.Join(relDir, ".keep"))
	if err != nil {
		return nil, err
	}
	return &Store{base: base, dir: filepath.Dir(dir)}, nil
}

func (s *Store) paths(key string) (metaPath, blobPath string, err error) {
	if len(key) < 4 {
		return "", "", clock.NewError("cache", clock.CatInvalidParam,
			"cache key too short")
	}
	// Shard by first two hex chars to keep directory entries bounded.
	rel := filepath.Join(s.relDir(), key[0:2], key)
	metaPath, err = fsx.Resolve(s.base, rel+".json")
	if err != nil {
		return "", "", err
	}
	blobPath, err = fsx.Resolve(s.base, rel+".bin")
	if err != nil {
		return "", "", err
	}
	return metaPath, blobPath, nil
}

func (s *Store) relDir() string {
	rel, err := filepath.Rel(s.base, s.dir)
	if err != nil {
		return filepath.Base(s.dir)
	}
	return rel
}

// Put writes the blob and its verification entry atomically. The artifact
// digest is computed over the blob and stored for later re-verification.
func (s *Store) Put(e Entry, blob []byte) error {
	if e.Key == "" {
		return clock.NewError("cache", clock.CatInvalidParam, "empty cache key")
	}
	e.ArtifactDigest = DigestBytes(blob)
	metaPath, blobPath, err := s.paths(e.Key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return clock.NewError("cache", clock.CatInvalidParam,
			"cache mkdir failed").Wrap(err)
	}
	if err := atomicWrite(blobPath, blob, 0o644); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return clock.NewError("cache", clock.CatCheckpoint,
			"cache entry marshal failed").Wrap(err)
	}
	return atomicWrite(metaPath, meta, 0o644)
}

// Get validates a cached entry against the expected source identity, input
// digest and index digest, and re-hashes the blob. Any mismatch (or a
// corrupt/missing entry) discards both files and returns a cache_mismatch.
func (s *Store) Get(key, sourceID, inputDigest, indexDigest string) ([]byte, *Entry, error) {
	metaPath, blobPath, err := s.paths(key)
	if err != nil {
		return nil, nil, err
	}
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, nil, clock.NewError("cache", clock.CatNotFound,
			"cache miss for key %s", key[:8]).Wrap(err)
	}
	var e Entry
	if err := json.Unmarshal(meta, &e); err != nil {
		return s.discard(metaPath, blobPath, "unreadable entry json")
	}
	blob, err := os.ReadFile(blobPath)
	if err != nil {
		return s.discard(metaPath, blobPath, "missing blob")
	}
	if e.SourceID != sourceID || e.InputDigest != inputDigest ||
		e.IndexDigest != indexDigest || e.Key != key ||
		e.ArtifactDigest != DigestBytes(blob) {
		return s.discard(metaPath, blobPath,
			"source/input/index/artifact digest mismatch")
	}
	return blob, &e, nil
}

func (s *Store) discard(metaPath, blobPath, reason string) ([]byte, *Entry, error) {
	_ = os.Remove(metaPath)
	_ = os.Remove(blobPath)
	return nil, nil, clock.NewError("cache", clock.CatCacheMismatch,
		"cache entry discarded: %s", reason)
}

// atomicWrite writes via a temp file in the same directory then renames, so
// readers never observe a half-written cache file.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return clock.NewError("cache", clock.CatCheckpoint,
			"temp create failed").Wrap(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return clock.NewError("cache", clock.CatCheckpoint,
			"temp write failed").Wrap(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return clock.NewError("cache", clock.CatCheckpoint,
			"temp chmod failed").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return clock.NewError("cache", clock.CatCheckpoint,
			"temp close failed").Wrap(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return clock.NewError("cache", clock.CatCheckpoint,
			"atomic rename failed").Wrap(err)
	}
	return nil
}
