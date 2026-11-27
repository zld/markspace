// Package derive stores content-addressed derived artifacts (reports, MP4
// bundles) on disk. Every artifact is keyed by its own SHA-256, and records
// the source digest it was derived from. A changed source digest simply has
// no matching entry, so stale derivatives are ignored rather than overwritten.
package derive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"media-check/internal/assets"
)

// ErrMiss means no valid derivative exists for the requested key.
var ErrMiss = errors.New("derive: no matching artifact")

// Descriptor is the on-disk metadata kept beside each artifact.
type Descriptor struct {
	// Key is the artifact's own content digest (its storage identity).
	Key string `json:"key"`
	// SourceDigest is the source asset the artifact was derived from.
	SourceDigest string `json:"source_digest"`
	// Kind labels the artifact type (e.g. "report", "mp4").
	Kind string `json:"kind"`
	Size int64  `json:"size"`
	// File is the artifact file name relative to the store root.
	File string `json:"file"`
}

// Store is a directory-backed content-addressable store.
type Store struct {
	root string
}

// NewStore opens (or creates) a derive store under root.
func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, fmt.Errorf("derive: create store: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o755); err != nil {
		return nil, fmt.Errorf("derive: create meta dir: %w", err)
	}
	return &Store{root: root}, nil
}

// Root exposes the store directory.
func (s *Store) Root() string { return s.root }

// Put writes bytes as a content-addressed artifact bound to one source.
// Writing the same content twice is idempotent and reuses the same key.
func (s *Store) Put(kind string, src *assets.Source, data []byte) (*Descriptor, error) {
	if err := src.Verify(); err != nil {
		return nil, err
	}
	key := assets.Digest(data)
	blob := s.blobPath(key)
	if _, err := os.Stat(blob); errors.Is(err, os.ErrNotExist) {
		tmp := blob + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return nil, fmt.Errorf("derive: write blob: %w", err)
		}
		if err := os.Rename(tmp, blob); err != nil {
			return nil, fmt.Errorf("derive: commit blob: %w", err)
		}
	}
	d := &Descriptor{
		Key: key, SourceDigest: src.ID, Kind: kind,
		Size: int64(len(data)), File: "blobs/" + key,
	}
	if err := s.writeMeta(d); err != nil {
		return nil, err
	}
	return d, nil
}

// Get returns an artifact only when both its key and source digest match.
// A changed source therefore yields ErrMiss instead of a stale derivative.
func (s *Store) Get(kind, sourceDigest string) (*Descriptor, []byte, error) {
	entries, err := s.listMeta()
	if err != nil {
		return nil, nil, err
	}
	for _, d := range entries {
		if d.Kind != kind || d.SourceDigest != sourceDigest {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, d.File))
		if err != nil {
			return nil, nil, fmt.Errorf("derive: read blob: %w", err)
		}
		// Verify content on every read; corruption must never pass silently.
		if assets.Digest(data) != d.Key {
			return nil, nil, fmt.Errorf("derive: blob %s failed content verification", d.Key)
		}
		return d, data, nil
	}
	return nil, nil, fmt.Errorf("%w: kind=%s source=%s", ErrMiss, kind, short(sourceDigest))
}

// InvalidateSource removes every artifact derived from the given source.
func (s *Store) InvalidateSource(sourceDigest string) (int, error) {
	entries, err := s.listMeta()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, d := range entries {
		if d.SourceDigest != sourceDigest {
			continue
		}
		_ = os.Remove(filepath.Join(s.root, d.File))
		_ = os.Remove(s.metaPath(d.Key))
		removed++
	}
	return removed, nil
}

func (s *Store) blobPath(key string) string  { return filepath.Join(s.root, "blobs", key) }
func (s *Store) metaPath(key string) string  { return filepath.Join(s.root, "meta", key+".json") }

func (s *Store) writeMeta(d *Descriptor) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.metaPath(d.Key) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("derive: write meta: %w", err)
	}
	return os.Rename(tmp, s.metaPath(d.Key))
}

func (s *Store) listMeta() ([]*Descriptor, error) {
	dir := filepath.Join(s.root, "meta")
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("derive: list meta: %w", err)
	}
	var out []*Descriptor
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, err
		}
		var d Descriptor
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("derive: corrupt meta %s: %w", f.Name(), err)
		}
		out = append(out, &d)
	}
	return out, nil
}

func short(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}
