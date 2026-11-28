package index

import (
	"encoding/json"
	"os"
	"path/filepath"

	"releasepipe/internal/errs"
)

// AssetRecord is one published asset: identity, platform, the digest of
// everything that produced it, and its output location.
type AssetRecord struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
	Output   string `json:"output"`
}

type indexFile struct {
	CommitCount int                    `json:"commit_count"`
	Assets      map[string]AssetRecord `json:"assets"`
}

// Store is the local asset index. Staged records become visible only
// after Commit, which atomically replaces the index file.
type Store struct {
	dir    string
	data   indexFile
	staged map[string]AssetRecord
}

// OpenStore loads (or initializes) the index under dir.
func OpenStore(dir string) (*Store, error) {
	s := &Store{dir: dir, staged: map[string]AssetRecord{}}
	s.data.Assets = map[string]AssetRecord{}
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, errs.Wrap(errs.StageIndex, errs.CodeInternal, dir, -1, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, errs.New(errs.StageIndex, errs.CodeChecksum, dir, 0,
			"index.json corrupt: %v", err)
	}
	if s.data.Assets == nil {
		s.data.Assets = map[string]AssetRecord{}
	}
	return s, nil
}

// Lookup returns the committed record for id when its digest matches —
// i.e. the asset can be reused without recomputation.
func (s *Store) Lookup(id, digest string) (AssetRecord, bool) {
	rec, ok := s.data.Assets[id]
	if !ok || rec.Digest != digest {
		return AssetRecord{}, false
	}
	return rec, true
}

// Stage adds or replaces a record in the pending batch.
func (s *Store) Stage(rec AssetRecord) { s.staged[rec.ID] = rec }

// CommitCount reports how many commits have landed.
func (s *Store) CommitCount() int { return s.data.CommitCount }

// Assets returns the committed records.
func (s *Store) Assets() map[string]AssetRecord { return s.data.Assets }

// Commit merges staged records and atomically persists the index.
func (s *Store) Commit() error {
	for id, rec := range s.staged {
		s.data.Assets[id] = rec
	}
	s.staged = map[string]AssetRecord{}
	s.data.CommitCount++
	return atomicWriteJSON(s.dir, "index.json", s.data)
}

// atomicWriteJSON writes name under dir via a temp file + rename, so a
// crash never leaves a half-written file behind.
func atomicWriteJSON(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, dir, -1, err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, name, -1, err)
	}
	tmp := filepath.Join(dir, name+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, tmp, -1, err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, tmp, -1, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, tmp, -1, err)
	}
	if err := f.Close(); err != nil {
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, tmp, -1, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return errs.Wrap(errs.StageIndex, errs.CodeInternal, name, -1, err)
	}
	return nil
}
