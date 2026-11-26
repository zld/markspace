// Package cache stores derived artifacts keyed by a digest of their origin,
// the processing parameters and a format version. A hit is only accepted after
// re-verifying both the origin and the stored content digest; anything that
// does not match is discarded and recomputed.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rtx/internal/model"
)

// FormatVersion is bumped whenever a cached artifact's meaning changes. It is
// part of every key so stale artifacts from an older build are never reused.
const FormatVersion = "rtx-cache-v1"

// Code values.
const (
	// CodeCacheCorrupt marks a cache entry whose stored content does not match
	// its recorded digest.
	CodeCacheCorrupt model.Code = "cache_corrupt"
	// CodeCacheSourceChanged marks an entry whose origin no longer matches.
	CodeCacheSourceChanged model.Code = "cache_source_changed"
)

// Params are the processing parameters that influence an artifact's bytes.
// They are canonicalised into the key so changing a flag recomputes.
type Params struct {
	MediaRateNum int64  `json:"media_rate_num"`
	MediaRateDen int64  `json:"media_rate_den"`
	VideoTrackID uint32 `json:"video_track_id"`
	AudioTrackID uint32 `json:"audio_track_id"`
	// Repair enables tfdt repair; a run with it off must not read entries
	// produced with it on.
	Repair bool `json:"repair"`
}

// KeyInput identifies one artifact: what it is, where it came from, and how it
// was processed.
type KeyInput struct {
	Kind       string   `json:"kind"`
	Source     string   `json:"source"`
	SourcePath string   `json:"source_path"`
	SourceSum  [32]byte `json:"source_sum"`
	Params     Params   `json:"params"`
}

// Key is the canonical cache key.
type Key struct {
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	Sum     string `json:"source_sum"`
	Params  Params `json:"params"`
}

// NewKey renders the canonical key for an input.
func NewKey(in KeyInput) Key {
	return Key{
		Version: FormatVersion,
		Kind:    in.Kind,
		Source:  in.Source,
		Path:    in.SourcePath,
		Sum:     hex.EncodeToString(in.SourceSum[:]),
		Params:  in.Params,
	}
}

// String is the hex digest of the canonical key JSON, used as the file name.
func (k Key) String() string {
	data, err := json.Marshal(k)
	if err != nil {
		// Key contains only marshalable fields; this cannot happen.
		panic(fmt.Sprintf("cache: marshal key: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Entry is the on-disk artifact plus the metadata needed to re-verify it.
type Entry struct {
	Key         Key    `json:"key"`
	ContentSum  string `json:"content_sum"`
	ContentSize int64  `json:"content_size"`
	// Payload is the raw artifact the caller produced. It is stored as-is so
	// the content digest covers exactly what will be consumed.
	Payload []byte `json:"-"`
	// PayloadFile is where the payload lives on disk.
	payloadPath string
	// recomputed records that the entry was rebuilt after a failed
	// verification, so the run can report it.
	recomputed bool
}

// Cache is a directory-backed content-addressed store.
type Cache struct {
	dir string
}

// Open creates (or reuses) a cache directory.
func Open(dir string) (*Cache, error) {
	if dir == "" {
		return nil, model.E(model.CodeInvalidInput, "open cache", "empty cache directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, model.E(model.CodeInvalidInput, "open cache", "%v", err).WithPath(dir).WithCause(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, model.E(model.CodeInvalidInput, "open cache", "not a directory").WithPath(dir)
	}
	return &Cache{dir: dir}, nil
}

// Dir returns the backing directory.
func (c *Cache) Dir() string { return c.dir }

// maxEntryBytes bounds a cached artifact so a corrupt or hostile index cannot
// make a run read unbounded data.
const maxEntryBytes = 64 << 20

func (c *Cache) payloadPath(key Key) string {
	return filepath.Join(c.dir, "content", key.String()+".bin")
}

func (c *Cache) metaPath(key Key) string {
	return filepath.Join(c.dir, "meta", key.String()+".json")
}

// WasRecomputed reports whether a Put replaced an entry that failed
// verification rather than creating a new one.
func (e *Entry) WasRecomputed() bool { return e.recomputed }

// A mismatch in the recorded origin digest, the content digest, the size, or
// the format version is reported as a miss with a reason so the caller can
// recompute; it never returns unverified bytes as a hit.
type GetResult struct {
	Entry  *Entry
	Reason string
}

// Get verifies a candidate entry. It returns (nil, reason, nil) for a miss and
// only returns an error for I/O problems that are not corruption.
func (c *Cache) Get(key Key, currentSourceSum [32]byte) (GetResult, error) {
	if key.Version != FormatVersion {
		return GetResult{Reason: "format version changed"}, nil
	}
	metaData, err := os.ReadFile(c.metaPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return GetResult{Reason: "miss"}, nil
	}
	if err != nil {
		return GetResult{}, model.E(CodeCacheCorrupt, "read cache meta", "%v", err).WithPath(c.metaPath(key)).WithCause(err)
	}
	var e Entry
	if err := json.Unmarshal(metaData, &e); err != nil {
		return GetResult{Reason: "corrupt metadata: " + err.Error()}, nil
	}
	if e.Key != key {
		return GetResult{Reason: "key mismatch"}, nil
	}
	if e.Key.Sum != hex.EncodeToString(currentSourceSum[:]) {
		return GetResult{Reason: "source digest changed"}, nil
	}
	if e.ContentSize > maxEntryBytes || e.ContentSize < 0 {
		return GetResult{Reason: "stored size out of range"}, nil
	}
	payload, err := os.ReadFile(c.payloadPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return GetResult{Reason: "missing payload"}, nil
	}
	if err != nil {
		return GetResult{}, model.E(CodeCacheCorrupt, "read cache payload", "%v", err).WithPath(c.payloadPath(key)).WithCause(err)
	}
	if int64(len(payload)) != e.ContentSize {
		return GetResult{Reason: fmt.Sprintf("size %d != recorded %d", len(payload), e.ContentSize)}, nil
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != e.ContentSum {
		return GetResult{Reason: "content digest mismatch"}, nil
	}
	e.Payload = payload
	e.payloadPath = c.payloadPath(key)
	return GetResult{Entry: &e}, nil
}

// Invalidate removes a poisoned entry. A missing entry is not an error.
func (c *Cache) Invalidate(key Key) error {
	var errs []string
	for _, p := range []string{c.metaPath(key), c.payloadPath(key)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(errs) > 0 {
		return model.E(CodeCacheCorrupt, "invalidate cache", "%s", strings.Join(errs, "; "))
	}
	return nil
}

// Put writes an entry atomically: payload first, then metadata. A crash between
// the two leaves an unreferenced payload, which Get treats as a miss.
func (c *Cache) Put(key Key, sourceSum [32]byte, payload []byte) (*Entry, error) {
	if int64(len(payload)) > maxEntryBytes {
		return nil, model.E(model.CodeFragmentTooBig, "cache put", "%d bytes exceeds cache limit %d", len(payload), maxEntryBytes)
	}
	if key.Sum != hex.EncodeToString(sourceSum[:]) {
		return nil, model.E(CodeCacheSourceChanged, "cache put", "key source digest does not match payload origin")
	}
	if err := os.MkdirAll(filepath.Join(c.dir, "content"), 0o755); err != nil {
		return nil, model.E(CodeCacheCorrupt, "cache put", "%v", err)
	}
	if err := os.MkdirAll(filepath.Join(c.dir, "meta"), 0o755); err != nil {
		return nil, model.E(CodeCacheCorrupt, "cache put", "%v", err)
	}
	sum := sha256.Sum256(payload)
	e := Entry{Key: key, ContentSum: hex.EncodeToString(sum[:]), ContentSize: int64(len(payload))}
	if err := writeAtomic(c.payloadPath(key), payload); err != nil {
		return nil, model.E(CodeCacheCorrupt, "cache put payload", "%v", err).WithPath(c.payloadPath(key))
	}
	meta, err := json.Marshal(e)
	if err != nil {
		return nil, model.E(CodeCacheCorrupt, "cache put meta", "%v", err)
	}
	if err := writeAtomic(c.metaPath(key), meta); err != nil {
		return nil, model.E(CodeCacheCorrupt, "cache put meta", "%v", err).WithPath(c.metaPath(key))
	}
	e.Payload = payload
	e.payloadPath = c.payloadPath(key)
	return &e, nil
}

// writeAtomic writes to a temp file in the destination directory, fsyncs, and
// renames, so a reader never observes a partial file.
func writeAtomic(path string, data []byte) error {
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
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
