// Package cache implements the content-addressed block cache.
//
// An entry key is the SHA-256 of (source id, input digest, processing
// parameters). On a hit the stored entry is still checked against all three
// components; any mismatch is a rejection, never a silent reuse, and the caller
// recomputes the block. Statistics record hits, misses, rejections and the
// bytes served from cache.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zld/media-regress/internal/error"
)

// Params are the processing parameters that affect block output. They are
// serialized deterministically so equal parameters always hash equally.
type Params struct {
	SampleRate   uint32 `json:"sample_rate"`
	Layout       string `json:"layout"`
	LoudnessLUFS string `json:"loudness"`
	GainDB       string `json:"gain_db"`
}

// Normalize canonicalises the parameter set for hashing.
func (p Params) Normalize() Params {
	return Params{
		SampleRate:   p.SampleRate,
		Layout:       strings.TrimSpace(p.Layout),
		LoudnessLUFS: strings.TrimSpace(p.LoudnessLUFS),
		GainDB:       strings.TrimSpace(p.GainDB),
	}
}

// Key returns the stable identity string.
func (p Params) Key() string {
	n := p.Normalize()
	return fmt.Sprintf("rate=%d;layout=%s;loudness=%s;gain=%s", n.SampleRate, n.Layout, n.LoudnessLUFS, n.GainDB)
}

// Key is the full cache key for one block.
type Key struct {
	Source string
	Input  string // digest of the source bytes
	Block  int
	Params string
}

// String renders the key deterministically.
func (k Key) String() string {
	return fmt.Sprintf("source=%s;input=%s;block=%d;%s", k.Source, k.Input, k.Block, k.Params)
}

// Digest returns the SHA-256 of the key string.
func (k Key) Digest() string {
	sum := sha256.Sum256([]byte(k.String()))
	return hex.EncodeToString(sum[:])
}

// Entry is the stored block plus its provenance.
type Entry struct {
	Key          string `json:"key"`
	Source       string `json:"source"`
	Input        string `json:"input"`
	Block        int    `json:"block"`
	Params       string `json:"params"`
	OutputDigest string `json:"output_digest"`
	Bytes        int    `json:"bytes"`
	Payload      []byte `json:"payload"`
}

// Stats counts cache outcomes.
type Stats struct {
	Hits        int
	Misses      int
	Rejected    int
	Stored      int
	BytesServed int
	Recompute   int
}

// Store is a filesystem cache rooted at a directory.
type Store struct {
	dir   string
	stats Stats
}

// Open creates the cache directory if needed.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, merr.New(merr.StageCache, merr.KindParse, 0, 0, "cache directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "create cache directory")
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(digest string) string {
	// Two-level fanout keeps directory sizes bounded.
	return filepath.Join(s.dir, digest[:2], digest[2:]+".json")
}

// DigestOf returns the SHA-256 of payload bytes.
func DigestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Put stores an entry, computing its output digest.
func (s *Store) Put(key Key, payload []byte) (Entry, error) {
	if len(payload) == 0 {
		return Entry{}, merr.New(merr.StageCache, merr.KindParse, 0, 0, "refusing to cache an empty block")
	}
	e := Entry{
		Key:          key.String(),
		Source:       key.Source,
		Input:        key.Input,
		Block:        key.Block,
		Params:       key.Params,
		OutputDigest: DigestOf(payload),
		Bytes:        len(payload),
		Payload:      append([]byte(nil), payload...),
	}
	data, err := json.Marshal(e)
	if err != nil {
		return Entry{}, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "encode cache entry")
	}
	p := s.path(key.Digest())
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return Entry{}, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "create cache shard")
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Entry{}, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "write cache entry")
	}
	if err := os.Rename(tmp, p); err != nil {
		return Entry{}, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "commit cache entry")
	}
	s.stats.Stored++
	return e, nil
}

// Get returns a validated entry. A miss, a provenance mismatch, a payload
// digest mismatch or a malformed file all yield ok=false with the appropriate
// counter advanced; the caller then recomputes. Only an I/O error that makes
// the cache itself unusable is returned as an error.
func (s *Store) Get(key Key) (Entry, bool, error) {
	p := s.path(key.Digest())
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		s.stats.Misses++
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "read cache entry")
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		s.stats.Rejected++
		s.stats.Recompute++
		return Entry{}, false, nil
	}
	if e.Key != key.String() || e.Source != key.Source || e.Input != key.Input ||
		e.Block != key.Block || e.Params != key.Params {
		s.stats.Rejected++
		s.stats.Recompute++
		return Entry{}, false, nil
	}
	if e.OutputDigest != DigestOf(e.Payload) || e.Bytes != len(e.Payload) {
		s.stats.Rejected++
		s.stats.Recompute++
		return Entry{}, false, nil
	}
	s.stats.Hits++
	s.stats.BytesServed += len(e.Payload)
	return e, true, nil
}

// Tamper rewrites an entry's payload without updating its digest. It exists so
// the regression run can prove that digest validation rejects poisoned cache
// content; it is not used in normal processing.
func (s *Store) Tamper(key Key) error {
	p := s.path(key.Digest())
	data, err := os.ReadFile(p)
	if err != nil {
		return merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "tamper: read entry")
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "tamper: decode entry")
	}
	for i := range e.Payload {
		e.Payload[i] ^= 0xff
	}
	out, err := json.Marshal(e)
	if err != nil {
		return merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "tamper: encode entry")
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		return merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "tamper: write entry")
	}
	return nil
}

// Stats returns the counters.
func (s *Store) Stats() Stats { return s.stats }

// Lookup reports whether an entry exists for key, regardless of validity.
func (s *Store) Lookup(key Key) bool {
	_, err := os.Stat(s.path(key.Digest()))
	return err == nil
}

// Keys lists cached key digests in stable order.
func (s *Store) Keys() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		out = append(out, strings.TrimSuffix(filepath.Base(path), ".json"))
		return nil
	})
	if err != nil {
		return nil, merr.Wrap(merr.StageCache, merr.KindParse, 0, 0, err, "list cache entries")
	}
	sort.Strings(out)
	return out, nil
}
