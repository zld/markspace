// Package cache provides a content-addressed, on-disk result cache. A cache
// key covers the input digest, source identity, codec parameters and every
// processing spec, so a hit can only ever correspond to identical work. Even
// on a hit the stored source and content digests are re-verified; a mismatch
// invalidates the entry and forces recomputation.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/mediad/internal/model"
)

// Spec is the complete parameter set whose variation must change the key.
// All fields are value-comparable after JSON canonicalization.
type Spec struct {
	SourceID       string            `json:"sourceId"`
	InputDigest    string            `json:"inputDigest"`
	Codec          string            `json:"codec"`
	SourceRate     uint32            `json:"sourceRate"`
	SourceChannels uint8             `json:"sourceChannels"`
	TargetRate     uint32            `json:"targetRate"`
	Layout         string            `json:"layout"`
	LUFS           float64           `json:"lufs"`
	TruePeak       float64           `json:"truePeak"`
	Extra          map[string]string `json:"extra,omitempty"`
}

// Entry is the on-disk cache record. It stores the digests needed to re-verify
// a hit alongside the opaque processed payload and its digest.
type Entry struct {
	Spec         Spec   `json:"spec"`
	SourceDigest string `json:"sourceDigest"`
	ContentDigest string `json:"contentDigest"`
	Payload      []byte `json:"payload"`
}

// Cache is a directory-backed content-addressed store.
type Cache struct {
	dir string
}

// New opens (creating if needed) a cache rooted at dir, constrained to that
// directory: keys are hashes and never contain path separators.
func New(dir string) (*Cache, error) {
	if dir == "" {
		return nil, fmt.Errorf("cache: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{dir: dir}, nil
}

// Key returns the hex SHA-256 content key for a spec.
func Key(s Spec) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // Spec is always JSON-marshalable
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (c *Cache) path(key string) string {
	// key is a 64-char hex string; filepath.Join keeps everything under dir.
	return filepath.Join(c.dir, key+".json")
}

// Put atomically writes an entry. Data is first written to a temp file in the
// same directory and renamed into place, so readers never observe a partial
// record.
func (c *Cache) Put(key string, e Entry) error {
	if filepath.Base(c.path(key)) != key+".json" {
		return model.NewError(model.CodeIllegalPacket, model.StageCache, 0, -1,
			"cache: unsafe key")
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	final := c.path(key)
	tmp, err := os.CreateTemp(c.dir, "tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, final)
}

// Get reads an entry and re-verifies its source and content digests. A missing
// entry returns (nil, nil); a digest mismatch returns a stable
// CACHE_DIGEST_MISMATCH error and the caller must recompute.
func (c *Cache) Get(key, sourceDigest, contentDigest string) (*Entry, error) {
	b, err := os.ReadFile(c.path(key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	if e.SourceDigest != sourceDigest || e.ContentDigest != contentDigest {
		return nil, model.NewError(model.CodeCacheDigestMismatch, model.StageCache,
			0, -1, "cache: stored digests do not match current source/content")
	}
	return &e, nil
}

// Invalidate removes an entry (e.g. after a detected mismatch). A missing file
// is not an error.
func (c *Cache) Invalidate(key string) error {
	err := os.Remove(c.path(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// DigestPayload returns the hex SHA-256 of a payload for content addressing.
func DigestPayload(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}
