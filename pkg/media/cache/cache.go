// Package cache implements versioned, content-addressed parse and index
// caches. Two key shapes enforce the invalidation rules:
//
//   - ParseKey binds a file's path identity, its SHA-256 content hash and the
//     parser version. Replacing a file at the same path changes its content
//     hash (and usually size/mtime), so the parse cache misses.
//   - IndexKey extends the parse key with the corrected timeline, manifest
//     metadata and an index version. A metadata-only change leaves the parse
//     key intact (cache hit) but changes the index key, forcing an index
//     rebuild without re-parsing media.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"cmafrepair/pkg/media/timebase"
)

// Version constants are bumped whenever the on-disk record layout changes,
// which independently invalidates every cached entry of that generation.
const (
	ParseVersion = "cmaf-parse-v1"
	IndexVersion = "cmaf-index-v1"
)

// FileIdentity captures how a file is addressed plus the stat facts that a
// same-path replacement changes even before content is hashed.
type FileIdentity struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time_unix_nano"`
	Inode   uint64 `json:"inode"`
	Device  int64  `json:"device"`
}

// IdentityOf stats path and records its identity.
func IdentityOf(path string) (FileIdentity, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, timebase.NewError(timebase.CodeIO, timebase.PhaseCache,
			"stat %s: %v", path, err)
	}
	id := FileIdentity{
		Path:    filepath.Clean(path),
		Size:    st.Size(),
		ModTime: st.ModTime().UTC().UnixNano(),
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		id.Inode = sys.Ino
		id.Device = int64(sys.Dev)
	}
	return id, nil
}

// ContentHash streams a SHA-256 over r without buffering the whole file.
func ContentHash(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, timebase.NewError(timebase.CodeIO, timebase.PhaseCache,
			"hash: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ContentHashFile hashes a file by path.
func ContentHashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", timebase.NewError(timebase.CodeIO, timebase.PhaseCache, "open %s: %v", path, err)
	}
	defer f.Close()
	sum, size, err := ContentHash(f)
	if err != nil {
		return "", err
	}
	st, err := f.Stat()
	if err == nil && st.Size() != size {
		return "", timebase.NewError(timebase.CodeIO, timebase.PhaseCache,
			"%s changed while hashing", path)
	}
	return sum, nil
}

// canonicalHash hashes the deterministic JSON encoding of v.
func canonicalHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// All key inputs are plain structs; a marshal failure is a bug.
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ParseKey is the cache key for parsed init/media objects.
type ParseKey struct {
	Identity     FileIdentity `json:"identity"`
	ContentHash  string       `json:"content_hash"`
	ParseVersion string       `json:"parse_version"`
}

// NewParseKey assembles a parse key.
func NewParseKey(id FileIdentity, contentHash string) ParseKey {
	return ParseKey{Identity: id, ContentHash: contentHash, ParseVersion: ParseVersion}
}

// Hash returns the stable hex digest used as the on-disk cache name.
func (k ParseKey) Hash() string { return canonicalHash(k) }

// IndexKey is the cache key for random-access indexes. It folds in the
// corrected timeline and manifest metadata so either forces a rebuild.
type IndexKey struct {
	ParseKeyHash     string `json:"parse_key_hash"`
	TimelineHash     string `json:"timeline_hash"`
	ManifestMetaHash string `json:"manifest_meta_hash"`
	IndexVersion     string `json:"index_version"`
}

// NewIndexKey assembles an index key from a parse key and the two extra
// fingerprints.
func NewIndexKey(parse ParseKey, timelineHash, manifestMetaHash string) IndexKey {
	return IndexKey{
		ParseKeyHash:     parse.Hash(),
		TimelineHash:     timelineHash,
		ManifestMetaHash: manifestMetaHash,
		IndexVersion:     IndexVersion,
	}
}

// Hash returns the stable hex digest for the index entry.
func (k IndexKey) Hash() string { return canonicalHash(k) }

// Outcome reports why a lookup hit or missed, for terminal reporting.
type Outcome struct {
	Hit    bool
	Reason string
}

// Stats accumulates cache activity for a run.
type Stats struct {
	Hits   int
	Misses int
}

// Store is a directory-backed, content-addressed blob cache. Each record is
// one small file named by the key hash, so memory use stays bounded by the
// individual record, never the cache size.
type Store struct {
	dir   string
	stats Stats
}

// NewStore creates/opens a cache directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, timebase.NewError(timebase.CodeIO, timebase.PhaseCache,
			"mkdir cache %s: %v", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Stats returns a copy of the running counters.
func (s *Store) Stats() Stats { return s.stats }

func (s *Store) path(hash string) string {
	// Shard by the first two hex chars to keep directories small.
	return filepath.Join(s.dir, hash[:2], hash)
}

// Get returns a record and whether it was present.
func (s *Store) Get(hash string) ([]byte, Outcome) {
	if hash == "" {
		s.stats.Misses++
		return nil, Outcome{Hit: false, Reason: "empty-key"}
	}
	b, err := os.ReadFile(s.path(hash))
	if err != nil {
		s.stats.Misses++
		if os.IsNotExist(err) {
			return nil, Outcome{Hit: false, Reason: "no-record"}
		}
		return nil, Outcome{Hit: false, Reason: "io-error"}
	}
	s.stats.Hits++
	return b, Outcome{Hit: true, Reason: "hit"}
}

// Put writes a record through a temp file and atomic rename.
func (s *Store) Put(hash string, payload []byte) error {
	if hash == "" {
		return timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseCache,
			"refusing to cache under an empty key")
	}
	dir := filepath.Dir(s.path(hash))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCache, "mkdir: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-cache-*")
	if err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCache, "temp: %v", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(name)
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCache, "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path(hash)); err != nil {
		os.Remove(name)
		return timebase.NewError(timebase.CodeIO, timebase.PhaseCache, "rename: %v", err)
	}
	return nil
}

// Fingerprint hashes arbitrary serializable metadata (timeline/manifest).
func Fingerprint(v any) string { return canonicalHash(v) }

// ModTime is re-exported for callers building freshness reports.
func ModTime(t time.Time) int64 { return t.UTC().UnixNano() }
