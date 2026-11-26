package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"medtrace/internal/core"
	"medtrace/internal/manifest"
)

// cacheVersion is bumped when the on-disk summary layout changes, so an old
// cache is treated as invalid rather than misread.
const cacheVersion = 1

// CacheEntry is the persisted summary for one scan. It stores the input digest,
// the per-segment digests and the epoch boundaries, but never the full sample
// list: the cache exists to decide whether a rescan is needed and to validate
// per-segment hashes, not to replace the index.
type CacheEntry struct {
	Version     int           `json:"version"`
	Manifest    string        `json:"manifest"`
	InputDigest string        `json:"input_digest"`
	Segments    []SegmentHash `json:"segments"`
	Epochs      []EpochRecord `json:"epochs"`
}

// SegmentHash is the per-segment digest and canonical bounds stored in the
// cache and emitted by the report.
type SegmentHash struct {
	URI     string `json:"uri"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
	Epoch   int    `json:"epoch"`
}

// EpochRecord persists one epoch boundary.
type EpochRecord struct {
	Number         int    `json:"number"`
	Reason         string `json:"reason"`
	Source         string `json:"source"`
	RawBase        int64  `json:"raw_base"`
	CanonicalNS    int64  `json:"canonical_ns"`
	BoundaryOffset int64  `json:"boundary_offset_ns"`
}

// InputDigest fingerprints a manifest and every file it references. It folds
// in each segment's size and modification time so an unchanged cache can be
// reused without hashing media, and a changed input invalidates it.
func InputDigest(root string, pl *manifest.Playlist) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "manifest:%s\n", pl.File)
	manifestPath, err := core.SafeJoin(root, pl.File)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(manifestPath); err == nil {
		fmt.Fprintf(h, "mtime:%d size:%d\n", st.ModTime().UnixNano(), st.Size())
	}
	inits := map[string]bool{}
	for _, r := range pl.Representations {
		if r.Init != "" {
			inits[r.Init] = true
		}
	}
	for _, uri := range sortedKeys(inits) {
		if err := digestFile(h, root, uri); err != nil {
			return "", err
		}
	}
	for _, s := range pl.Segments {
		if err := digestFile(h, root, s.URI); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// digestFile folds one file's identity into h.
func digestFile(h interface{ Write([]byte) (int, error) }, root, rel string) error {
	abs, err := core.SafeJoin(root, rel)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("digest %s: %w", rel, err)
	}
	fmt.Fprintf(h, "file:%s size:%d mtime:%d\n", rel, st.Size(), st.ModTime().UnixNano())
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BuildEntry assembles the cache entry for a completed scan.
func BuildEntry(pl *manifest.Playlist, digest string, idx *core.Index, epochs []core.Epoch) CacheEntry {
	entry := CacheEntry{
		Version:     cacheVersion,
		Manifest:    pl.File,
		InputDigest: digest,
	}
	for _, s := range idx.Segments {
		entry.Segments = append(entry.Segments, SegmentHash{
			URI: s.URI, SHA256: s.SHA256, Size: s.Size,
			StartNS: int64(s.Start), EndNS: int64(s.End), Epoch: s.Epoch,
		})
	}
	for _, e := range epochs {
		entry.Epochs = append(entry.Epochs, EpochRecord{
			Number: e.Number, Reason: string(e.Reason), Source: e.Source,
			RawBase: e.RawBase, CanonicalNS: int64(e.CanonicalBase),
			BoundaryOffset: int64(e.BoundaryOffset),
		})
	}
	return entry
}

// Cache is the on-disk summary store rooted at workDir/index.
type Cache struct {
	dir string
}

// NewCache returns a cache rooted at dir (created lazily on save).
func NewCache(dir string) *Cache { return &Cache{dir: dir} }

// path returns the cache file for a manifest, keyed by a hash of its name so
// two manifests can coexist and each keeps its own summary.
func (c *Cache) path(manifestRel string) string {
	sum := sha256.Sum256([]byte(manifestRel))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:8])+".json")
}

// Load reads the cached entry for a manifest. A missing file is (nil, nil);
// an unreadable or wrong-version file is discarded so the caller rescans.
func (c *Cache) Load(manifestRel string) (*CacheEntry, error) {
	data, err := os.ReadFile(c.path(manifestRel))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e CacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, nil
	}
	if e.Version != cacheVersion || e.Manifest != manifestRel {
		return nil, nil
	}
	return &e, nil
}

// Save writes the entry atomically (temp file + rename) so a crash mid-write
// cannot leave a half-written cache that a later run would trust.
func (c *Cache) Save(entry CacheEntry) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, "cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, c.path(entry.Manifest))
}

// Valid reports whether a cached entry may be reused for the given input
// digest. Reuse requires an identical digest; any change invalidates it.
func (e *CacheEntry) Valid(digest string) bool {
	return e != nil && e.Version == cacheVersion && e.InputDigest == digest
}
