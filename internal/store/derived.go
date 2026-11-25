package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"replaycheck/internal/model"
)

// ErrNotFound is returned when a content address has no cached entry.
var ErrNotFound = errors.New("derived entry not found")

// Derived is a content-addressed cache. Entries are keyed by a fingerprint of
// everything that determines them (source digest, normalized track digest,
// policy) so a changed source invalidates only its own derivatives.
type Derived struct {
	Root    string
	mu      sync.Mutex
	hits    int
	misses  int
	written int
}

func NewDerived(root string) *Derived { return &Derived{Root: root} }

// Key composes a cache key from labelled digests.
func Key(parts ...string) string { return model.Fingerprint(parts...) }

func (d *Derived) pathFor(key string) (string, error) {
	if !model.ValidDigest(key) {
		return "", model.NewError(model.StageDerive, "cache", model.TrackRef{}, 0, "cache key is not a valid digest")
	}
	dir := filepath.Join(d.Root, key[:2])
	return filepath.Join(dir, key+".json"), nil
}

// Get returns the cached bytes for key.
func (d *Derived) Get(key string) ([]byte, bool, error) {
	p, err := d.pathFor(key)
	if err != nil {
		return nil, false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		d.misses++
		return nil, false, nil
	}
	if err != nil {
		return nil, false, model.WrapError(model.StageDerive, "cache", model.TrackRef{}, 0, "cannot read cache entry", err)
	}
	d.hits++
	return b, true, nil
}

// Put writes value under key unless an entry already exists. The write is
// atomic (temp file + rename) so a crash cannot leave a half entry.
func (d *Derived) Put(key string, value any) error {
	p, err := d.pathFor(key)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return model.WrapError(model.StageDerive, "cache", model.TrackRef{}, 0, "cannot create cache dir", err)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return model.WrapError(model.StageDerive, "cache", model.TrackRef{}, 0, "cannot encode cache entry", err)
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return model.WrapError(model.StageDerive, "cache", model.TrackRef{}, 0, "cannot write cache entry", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return model.WrapError(model.StageDerive, "cache", model.TrackRef{}, 0, "cannot publish cache entry", err)
	}
	d.written++
	return nil
}

// Stats reports cache hit/miss counters for the acceptance summary.
type Stats struct {
	Hits    int `json:"hits"`
	Misses  int `json:"misses"`
	Written int `json:"written"`
}

func (d *Derived) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Stats{Hits: d.hits, Misses: d.misses, Written: d.written}
}
