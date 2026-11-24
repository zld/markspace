package mediacore

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// IndexCache persists an index next to derived artefacts and refuses to serve
// it once the input bytes change.
type IndexCache struct {
	Dir string
}

// NewIndexCache returns a cache rooted at dir.
func NewIndexCache(dir string) *IndexCache { return &IndexCache{Dir: dir} }

func (c *IndexCache) path() string { return filepath.Join(c.Dir, "index.json") }

// LoadResult describes a cache lookup.
type LoadResult struct {
	Index   *Index
	Rebuilt bool
	Reason  string
}

// Load returns a valid index for inputPath, rebuilding it with build when the
// cache is missing, unreadable, from an older schema or bound to different
// input bytes. A returned Rebuilt=true always means the digest changed or no
// usable cache existed; it never silently reuses stale data.
func (c *IndexCache) Load(inputPath string, build func() (*Index, error)) (*LoadResult, error) {
	digest, err := DigestFile(inputPath)
	if err != nil {
		return nil, err
	}
	cached, err := c.read()
	if err == nil && cached != nil {
		switch {
		case cached.SchemaVersion != IndexSchemaVersion:
			return c.rebuild(build, "schema changed")
		case cached.Digest.Content != digest.Content || cached.Digest.Size != digest.Size:
			return c.rebuild(build, "input digest changed")
		default:
			return &LoadResult{Index: cached, Reason: "cache hit"}, nil
		}
	}
	if err != nil && !os.IsNotExist(underlying(err)) {
		return c.rebuild(build, "cache unreadable: "+err.Error())
	}
	return c.rebuild(build, "cache missing")
}

// underlying unwraps classified errors to test os.IsNotExist.
func underlying(err error) error {
	if e, ok := err.(*Error); ok && e.Err != nil {
		return e.Err
	}
	return err
}

func (c *IndexCache) rebuild(build func() (*Index, error), reason string) (*LoadResult, error) {
	idx, err := build()
	if err != nil {
		return nil, err
	}
	if err := c.store(idx); err != nil {
		return nil, err
	}
	return &LoadResult{Index: idx, Rebuilt: true, Reason: reason}, nil
}

func (c *IndexCache) read() (*Index, error) {
	data, err := os.ReadFile(c.path())
	if err != nil {
		return nil, wrapErr(KindIO, "cache", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, wrapErr(KindIndex, "cache", err)
	}
	return &idx, nil
}

func (c *IndexCache) store(idx *Index) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return wrapErr(KindIO, "cache", err)
	}
	data, err := idx.CanonicalJSON()
	if err != nil {
		return wrapErr(KindIndex, "cache", err)
	}
	// Write to a temporary file and rename so a crash never leaves a
	// half-written index that would later look valid.
	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return wrapErr(KindIO, "cache", err)
	}
	if err := os.Rename(tmp, c.path()); err != nil {
		return wrapErr(KindIO, "cache", err)
	}
	return nil
}

// Invalidate removes the cached index.
func (c *IndexCache) Invalidate() error {
	if err := os.Remove(c.path()); err != nil && !os.IsNotExist(err) {
		return wrapErr(KindIO, "cache", err)
	}
	return nil
}
