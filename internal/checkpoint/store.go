// Package checkpoint persists pipeline state atomically and records exactly
// which inputs, decisions and outputs produced each result.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mediapipeline/internal/media"
)

// Schema is the checkpoint on-disk version.
const Schema = 1

// SegmentRef ties a produced segment to its manifest entry.
type SegmentRef struct {
	Index   int     `json:"index"`
	StartMS float64 `json:"start_ms"`
	EndMS   float64 `json:"end_ms"`
	SHA256  string  `json:"sha256"`
	Bytes   int64   `json:"bytes"`
}

// State is the durable pipeline state. It is only ever written whole, via a
// temporary file and an atomic rename, after the manifest itself is durable.
type State struct {
	Schema         int    `json:"schema"`
	InputSHA256    string `json:"input_sha256"`
	ManifestSHA256 string `json:"manifest_sha256"`
	// ProvenanceSHA256 names the provenance record describing this run. It is
	// written to disk before the checkpoint that carries it, so a checkpoint
	// can never point at provenance that did not survive.
	ProvenanceSHA256 string     `json:"provenance_sha256"`
	ContentID        string     `json:"content_id"`
	SealedSegments   int        `json:"sealed_segments"`
	BoundaryMS       float64    `json:"boundary_ms"`
	Representations  []RepState `json:"representations"`
	// Outputs lists every artifact written by this run, keyed by idempotency
	// key, so a repeated run can prove it produced nothing new.
	Outputs    []OutputRecord     `json:"outputs"`
	Provenance []ProvenanceRecord `json:"provenance"`
	// SegmentProvenance explains each produced segment individually, so a
	// single output can be audited without replaying the entire run.
	SegmentProvenance []SegmentProvenance `json:"segment_provenance"`
	UpdatedUnix       int64               `json:"updated_unix"`
}

// RepState is per-representation checkpoint progress.
type RepState struct {
	Name        string       `json:"name"`
	Segments    []SegmentRef `json:"segments"`
	ResumedFrom int          `json:"resumed_from"`
	Reused      int          `json:"reused"`
}

// IdempotencyKey derives the stable identity of an output from its logical
// role and content, so replaying the same input can never write twice.
func IdempotencyKey(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return kind + "-" + hex.EncodeToString(h.Sum(nil))[:24]
}

// Store is an on-disk checkpoint directory.
type Store struct {
	dir  string
	path string
}

// Open prepares dir and returns a Store rooted there.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, media.NewError(media.InvalidInput, "checkpoint directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, media.WrapError(media.InvalidInput, err, "creating checkpoint dir %s", dir)
	}
	return &Store{dir: dir, path: filepath.Join(dir, "state.json")}, nil
}

// Path returns the state file location.
func (s *Store) Path() string { return s.path }

// Load reads the current state. A missing file is not an error: it means the
// pipeline has never run in this directory.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, media.WrapError(media.CheckpointCorrupt, err, "reading %s", s.path)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		// A truncated or garbled checkpoint is recoverable: the pipeline
		// restarts from zero and rewrites it.
		return nil, media.WrapError(media.CheckpointCorrupt, err, "parsing %s", s.path)
	}
	if st.Schema != Schema {
		return nil, media.NewError(media.CheckpointCorrupt,
			"checkpoint schema %d unsupported (want %d)", st.Schema, Schema)
	}
	return &st, nil
}

// Save writes state atomically: temp file in the same directory, fsync, then
// rename. Readers therefore see either the old file or the new one, never a
// partial mixture.
func (s *Store) Save(st *State) error {
	if st == nil {
		return media.NewError(media.InvalidInput, "nil checkpoint state")
	}
	st.Schema = Schema
	st.UpdatedUnix = time.Now().Unix()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return media.WrapError(media.InvalidInput, err, "encoding checkpoint")
	}
	return writeFileAtomic(s.path, data)
}

// WriteManifest persists the manifest JSON atomically before the checkpoint
// that references it advances. That ordering is what makes the invariant
// "checkpoint never ahead of durable output" true.
func (s *Store) WriteManifest(name string, data []byte) (string, error) {
	path := filepath.Join(s.dir, name)
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// ReadManifest reads a previously written manifest.
func (s *Store) ReadManifest(name string) ([]byte, error) {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, media.NewError(media.CheckpointAhead,
				"checkpoint references manifest %s which is not on disk", name)
		}
		return nil, media.WrapError(media.CheckpointCorrupt, err, "reading %s", path)
	}
	return data, nil
}

// VerifyOrdering checks the checkpoint invariant: every output it claims is
// already durable, and any manifest it names exists with a matching hash.
func (s *Store) VerifyOrdering(st *State) error {
	if st == nil {
		return nil
	}
	for _, out := range st.Outputs {
		if out.Path == "" {
			return media.NewError(media.CheckpointAhead,
				"output %s has no path", out.Key)
		}
		info, err := os.Stat(filepath.Join(s.dir, out.Path))
		if err != nil {
			return media.NewError(media.CheckpointAhead,
				"output %s missing on disk: %v", out.Path, err)
		}
		if info.Size() != out.Bytes {
			return media.NewError(media.CheckpointAhead,
				"output %s is %d bytes on disk, checkpoint says %d",
				out.Path, info.Size(), out.Bytes)
		}
	}
	if err := s.verifyArtifact(st.ManifestSHA256, "manifest.json"); err != nil {
		return err
	}
	return s.verifyArtifact(st.ProvenanceSHA256, "provenance.json")
}

// verifyArtifact checks that a named artifact exists and hashes as claimed.
func (s *Store) verifyArtifact(want, name string) error {
	if want == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return media.NewError(media.CheckpointAhead,
			"checkpoint names %s hash %s but the file is unreadable: %v", name, want, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return media.NewError(media.CheckpointAhead,
			"%s hash %s does not match checkpoint %s", name, got, want)
	}
	return nil
}

// ReuseIndex indexes prior outputs by idempotency key.
type ReuseIndex struct {
	byKey map[string]OutputRecord
}

// BuildReuseIndex returns an index of outputs already recorded.
func BuildReuseIndex(st *State) *ReuseIndex {
	idx := &ReuseIndex{byKey: make(map[string]OutputRecord)}
	if st == nil {
		return idx
	}
	for _, o := range st.Outputs {
		idx.byKey[o.Key] = o
	}
	return idx
}

// Lookup returns the prior output for key, if any, and whether the content
// hash matches. A key hit with a different hash is an idempotency conflict:
// the same logical output would change, which the pipeline refuses to do
// silently.
func (idx *ReuseIndex) Lookup(key, contentHash string) (OutputRecord, bool, error) {
	prev, ok := idx.byKey[key]
	if !ok {
		return OutputRecord{}, false, nil
	}
	if prev.ContentSHA256 != contentHash {
		return prev, true, media.NewError(media.IdempotencyConflict,
			"output %s previously had hash %s, now %s",
			key, prev.ContentSHA256, contentHash)
	}
	return prev, true, nil
}

// writeFileAtomic writes data to a sibling temp file, fsyncs it, renames it
// over path, and fsyncs the directory so the rename itself is durable.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.WrapError(media.InvalidInput, err, "creating %s", dir)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return media.WrapError(media.InvalidInput, err, "creating temp file in %s", dir)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return media.WrapError(media.InvalidInput, err, "writing %s", tmpName)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.WrapError(media.InvalidInput, err, "syncing %s", tmpName)
	}
	if err := tmp.Close(); err != nil {
		return media.WrapError(media.InvalidInput, err, "closing %s", tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return media.WrapError(media.InvalidInput, err, "renaming %s to %s", tmpName, path)
	}
	tmpName = ""
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// SortOutputs orders outputs by key so serialised state is canonical.
func SortOutputs(outs []OutputRecord) {
	sort.Slice(outs, func(i, j int) bool { return outs[i].Key < outs[j].Key })
}
