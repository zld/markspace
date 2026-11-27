// Package manifest writes deterministic, content-addressed archive manifests.
// Serialization order is fully fixed (sorted entries, fixed JSON field order)
// so identical inputs always produce byte-identical manifests.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/model"
)

// Entry is one archived source's immutable manifest record.
type Entry struct {
	SourceSHA     string `json:"source_sha256"`
	Codec         string `json:"codec"`
	StreamSHA     string `json:"stream_sha256"`
	IndexSHA      string `json:"index_sha256"`
	StreamBytes   int64  `json:"stream_bytes"`
	Timescale     uint32 `json:"timescale"`
	SampleCount   int    `json:"sample_count"`
	KeyCount      int    `json:"key_count"`
	StartNS       int64  `json:"manifest_start_ns"`
	EndNS         int64  `json:"manifest_end_ns"`
	MaxAlignErrNS int64  `json:"alignment_error_ns"`
	TrackDigest   string `json:"track_sha256"`
	MP4Digest     string `json:"mp4_sha256"`
}

// Manifest is the on-disk archive index. Entries are always kept sorted by
// SourceSHA and never duplicated.
type Manifest struct {
	// Version is bumped only for incompatible layout changes.
	Version int      `json:"version"`
	Entries []*Entry `json:"entries"`
}

// CurrentVersion is the manifest layout version emitted by this build.
const CurrentVersion = 1

// New returns an empty manifest at the current version.
func New() *Manifest {
	return &Manifest{Version: CurrentVersion, Entries: nil}
}

// canonicalJSON serializes with fixed field order (struct order), no map
// iteration, and a trailing newline so file hashes are stable.
func canonicalJSON(m *Manifest) ([]byte, error) {
	// Defensive copy of the entry slice in fixed order; json.Marshal honors
	// struct field declaration order and slice order, never map order.
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Add inserts an entry, rejecting duplicate source digests and keeping the
// slice sorted. A duplicate is a structured error rather than a silent append,
// which is what prevents repeated deliveries from growing the manifest.
func (m *Manifest) Add(e *Entry) error {
	if e == nil || e.SourceSHA == "" {
		return model.New(model.ErrStageState, "", -1, "manifest entry requires a source digest")
	}
	i := sort.Search(len(m.Entries), func(i int) bool { return m.Entries[i].SourceSHA >= e.SourceSHA })
	if i < len(m.Entries) && m.Entries[i].SourceSHA == e.SourceSHA {
		return model.New(model.ErrDuplicateManifest, e.Codec, -1,
			"manifest already contains source %s", e.SourceSHA)
	}
	m.Entries = append(m.Entries, nil)
	copy(m.Entries[i+1:], m.Entries[i:])
	m.Entries[i] = e
	return nil
}

// Has reports whether a source digest is present.
func (m *Manifest) Has(sourceSHA string) bool {
	i := sort.Search(len(m.Entries), func(i int) bool { return m.Entries[i].SourceSHA >= sourceSHA })
	return i < len(m.Entries) && m.Entries[i].SourceSHA == sourceSHA
}

// Get returns the entry for a source digest, or nil.
func (m *Manifest) Get(sourceSHA string) *Entry {
	i := sort.Search(len(m.Entries), func(i int) bool { return m.Entries[i].SourceSHA >= sourceSHA })
	if i < len(m.Entries) && m.Entries[i].SourceSHA == sourceSHA {
		return m.Entries[i]
	}
	return nil
}

// Marshal renders the manifest to deterministic bytes.
func (m *Manifest) Marshal() ([]byte, error) {
	// Ensure ordering even if entries were mutated outside Add.
	sort.SliceStable(m.Entries, func(i, j int) bool { return m.Entries[i].SourceSHA < m.Entries[j].SourceSHA })
	return canonicalJSON(m)
}

// Digest returns the SHA-256 of the canonical manifest bytes.
func (m *Manifest) Digest() (string, error) {
	b, err := m.Marshal()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// WriteAtomically renders the manifest to a temp file in dir, fsyncs it and
// renames it into place, so readers never observe a half-written manifest.
// The temp file is removed on any failure.
func WriteAtomically(dir, name string, m *Manifest) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := m.Marshal()
	if err != nil {
		return "", err
	}
	final := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return "", err
	}
	return final, nil
}

// Load reads and parses a manifest, validating its version field.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadBytes(b)
}

// LoadBytes parses manifest bytes, validating the version field.
func LoadBytes(b []byte) (*Manifest, error) {
	m := &Manifest{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, model.New(model.ErrStageState, "", -1, "manifest parse failed: %v", err)
	}
	if m.Version != CurrentVersion {
		return nil, model.New(model.ErrStageState, "", -1,
			"manifest version %d unsupported, want %d", m.Version, CurrentVersion)
	}
	// Re-sort defensively and reject duplicates.
	sort.SliceStable(m.Entries, func(i, j int) bool { return m.Entries[i].SourceSHA < m.Entries[j].SourceSHA })
	for i := 1; i < len(m.Entries); i++ {
		if m.Entries[i].SourceSHA == m.Entries[i-1].SourceSHA {
			return nil, model.New(model.ErrDuplicateManifest, m.Entries[i].Codec, -1,
				"loaded manifest contains duplicate source %s", m.Entries[i].SourceSHA)
		}
	}
	return m, nil
}
