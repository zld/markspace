package cache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const IndexVersion = 1

// FileIdentity is stronger than path+mtime: replacement by rename produces a
// different device/inode pair even when size and timestamp are preserved.
type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Size   int64  `json:"size"`
	ModNS  int64  `json:"mod_ns"`
}

func IdentityForPath(path string) (FileIdentity, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	id := FileIdentity{Size: st.Size(), ModNS: st.ModTime().UnixNano()}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys != nil {
		id.Device = uint64(sys.Dev)
		id.Inode = uint64(sys.Ino)
	}
	return id, nil
}

type StructureRecord struct {
	Path        string       `json:"path"`
	Identity    FileIdentity `json:"identity"`
	Fingerprint string       `json:"fingerprint"`
	ContentSHA  string       `json:"content_sha256"`
	AssetID     string       `json:"asset_id"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type Index struct {
	Version int                        `json:"version"`
	Entries map[string]StructureRecord `json:"entries"`
}

func NewIndex() *Index {
	return &Index{Version: IndexVersion, Entries: map[string]StructureRecord{}}
}

func LoadIndex(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewIndex(), nil
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		// A torn/foreign index must not be trusted. Callers rebuild it.
		return NewIndex(), nil
	}
	if idx.Version != IndexVersion || idx.Entries == nil {
		return NewIndex(), nil
	}
	return &idx, nil
}

func CanonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Lookup requires path, file identity, and structural fingerprint to match.
func (idx *Index) Lookup(path string, id FileIdentity, fingerprint string) (StructureRecord, bool) {
	key := CanonicalPath(path)
	rec, ok := idx.Entries[key]
	if !ok {
		return StructureRecord{}, false
	}
	if rec.Identity != id || rec.Fingerprint != fingerprint {
		return StructureRecord{}, false
	}
	return rec, true
}

// Previous returns the old path record even if it no longer matches. This is
// used to invalidate content addressed by the previous file bytes.
func (idx *Index) Previous(path string) (StructureRecord, bool) {
	rec, ok := idx.Entries[CanonicalPath(path)]
	return rec, ok
}

func (idx *Index) Put(rec StructureRecord) {
	if rec.Path == "" {
		return
	}
	rec.Path = CanonicalPath(rec.Path)
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now().UTC()
	}
	idx.Entries[rec.Path] = rec
}

func SaveJSONAtomic(path string, v any, perm fs.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	return os.Rename(name, path)
}

func (idx *Index) Save(path string) error {
	return SaveJSONAtomic(path, idx, 0o644)
}
