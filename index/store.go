package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zld/mediapipe/media"
)

// Store persists indexes inside a caller-supplied work directory. A cached
// index is reused only when the input digest matches the recorded one and the
// stored payload still validates.
type Store struct {
	Root   string
	Limits media.Limits
}

// NewStore returns a store rooted at dir.
func NewStore(dir string, lim media.Limits) (*Store, error) {
	if dir == "" {
		return nil, media.NewError(media.KindArgument, "", "index store root is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, media.WrapError(media.KindIO, dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, media.WrapError(media.KindIO, abs, err)
	}
	return &Store{Root: abs, Limits: lim}, nil
}

// PathFor returns the on-disk index path for an input file. It is keyed by
// input digest so distinct inputs never share a cache entry.
func (s *Store) PathFor(inputSHA string) string {
	return filepath.Join(s.Root, inputSHA+"."+Version+".json")
}

// Outcome reports how an index was obtained.
type Outcome struct {
	Index     *Index
	Reused    bool   `json:"reused"`
	Rebuilt   bool   `json:"rebuilt"`
	CachePath string `json:"cache_path"`
	Reason    string `json:"reason,omitempty"`
}

// Scan builds or reuses an index for the given container.
func (s *Store) Scan(c *media.Container) (*Outcome, error) {
	return s.ScanWith(c, nil)
}

// ScanWith is Scan with an injectable probe so tests can use a builder.
func (s *Store) ScanWith(c *media.Container, _ any) (*Outcome, error) {
	cachePath := s.PathFor(c.SHA256)
	if cached, reason := s.load(cachePath, c.SHA256); cached != nil {
		return &Outcome{Index: cached, Reused: true, CachePath: cachePath}, nil
	} else if reason != "" {
		if err := s.discard(cachePath); err != nil {
			return nil, err
		}
	}
	idx, err := Build(c)
	if err != nil {
		return nil, err
	}
	if err := s.save(cachePath, idx); err != nil {
		return nil, err
	}
	return &Outcome{Index: idx, Rebuilt: true, CachePath: cachePath}, nil
}

func (s *Store) load(path, wantSHA string) (*Index, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ""
		}
		return nil, "unreadable cache: " + err.Error()
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, "malformed cache: " + err.Error()
	}
	if idx.InputSHA256 != wantSHA {
		return nil, fmt.Sprintf("cache is for input %s", idx.InputSHA256)
	}
	if err := idx.Validate(); err != nil {
		return nil, err.Error()
	}
	return &idx, ""
}

func (s *Store) save(path string, idx *Index) error {
	b, err := idx.Canonical()
	if err != nil {
		return err
	}
	return WriteAtomic(path, b)
}

func (s *Store) discard(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return media.WrapError(media.KindIO, path, err)
	}
	return nil
}

// WriteAtomic writes data to path via a temporary file in the same directory
// followed by an atomic rename. A partially written temp file can never be
// mistaken for a finished artifact, and temp names are excluded from outputs.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.WrapError(media.KindIO, dir, err)
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return media.WrapError(media.KindIO, dir, err)
	}
	tmp := f.Name()
	defer func() {
		// Best-effort cleanup; a leftover temp is ignored by consumers.
		_ = os.Remove(tmp)
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return media.WrapError(media.KindIO, tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return media.WrapError(media.KindIO, tmp, err)
	}
	if err := f.Close(); err != nil {
		return media.WrapError(media.KindIO, tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return media.WrapError(media.KindIO, path, err)
	}
	return nil
}

// IsTempName reports whether a basename is an in-progress temp file.
func IsTempName(name string) bool {
	return strings.HasPrefix(name, ".tmp-")
}
