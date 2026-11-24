package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CacheEntry is the on-disk manifest stored next to a cached artifact.
type CacheEntry struct {
	Key            CacheKey `json:"key"`
	SourceDigest   string   `json:"source_digest"`   // digest of the segment input data
	ArtifactPath   string   `json:"artifact_path"`   // basename inside the cache dir
	ArtifactDigest string   `json:"artifact_digest"` // digest of the produced file
	Params         string   `json:"params"`
}

// MissReason explains why a cache lookup did not return a usable artifact.
type MissReason string

const (
	MissNone             MissReason = ""
	MissNotCached        MissReason = "not_cached"
	MissKeyMismatch      MissReason = "key_mismatch"      // params or tool version changed
	MissSourceMismatch   MissReason = "source_mismatch"   // input digest differs
	MissArtifactMismatch MissReason = "artifact_mismatch" // stored file changed/corrupt
)

// Cache is a content-addressed artifact store rooted at one directory.
type Cache struct {
	root string
}

// NewCache creates (or reopens) a cache directory.
func NewCache(root string) (*Cache, error) {
	if _, err := SafeJoin(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Cache{root: root}, nil
}

// Root returns the cache root directory.
func (c *Cache) Root() string { return c.root }

func (c *Cache) manifestPath(hash string) (string, error) {
	return SafeJoin(c.root, hash+".json")
}

func (c *Cache) artifactPath(hash, name string) (string, error) {
	return SafeJoin(c.root, hash+"-"+name)
}

// Get verifies a cached entry end to end. On a miss it returns the reason so
// the caller can recompute and record why the cache was bypassed.
func (c *Cache) Get(key CacheKey, sourceDigest, artifactName string) (*CacheEntry, MissReason, error) {
	mp, err := c.manifestPath(key.Hash())
	if err != nil {
		return nil, MissNotCached, err
	}
	raw, err := os.ReadFile(mp)
	if os.IsNotExist(err) {
		return nil, MissNotCached, nil
	} else if err != nil {
		return nil, MissNotCached, err
	}
	var entry CacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, MissKeyMismatch, nil
	}
	if entry.Key.Hash() != key.Hash() || entry.Params != key.Params {
		return nil, MissKeyMismatch, nil
	}
	if entry.SourceDigest != sourceDigest {
		return nil, MissSourceMismatch, nil
	}
	ap, err := c.artifactPath(key.Hash(), artifactName)
	if err != nil {
		return nil, MissArtifactMismatch, err
	}
	got, err := FileDigest(ap)
	if err != nil || got != entry.ArtifactDigest {
		return nil, MissArtifactMismatch, nil
	}
	return &entry, MissNone, nil
}

// Put atomically stores an artifact and its manifest after recomputation.
// The artifact digest is computed from the real file, never assumed.
func (c *Cache) Put(key CacheKey, sourceDigest, artifactName string, artifact []byte) (*CacheEntry, error) {
	if _, err := c.artifactPath(key.Hash(), artifactName); err != nil {
		return nil, err
	}
	entry := CacheEntry{
		Key: key, SourceDigest: sourceDigest,
		ArtifactPath:   key.Hash() + "-" + artifactName,
		ArtifactDigest: DigestSHA256(artifact), Params: key.Params,
	}
	ap, err := c.artifactPath(key.Hash(), artifactName)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(ap, artifact, 0o644); err != nil {
		return nil, err
	}
	mp, err := c.manifestPath(key.Hash())
	if err != nil {
		return nil, err
	}
	manifest, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(mp, append(manifest, '\n'), 0o644); err != nil {
		return nil, err
	}
	return &entry, nil
}

// writeFileAtomic writes via a temp file in the same directory then renames,
// so a crash can never leave a half-written artifact or manifest.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
