// Package checkpoint persists per-chunk processing state and the local
// index. Checkpoints are content-addressed by input hash, manifest hash
// and rule version: a chunk is reused only when all three match and the
// chunk had previously passed validation. Writes are atomic via
// temp-file-then-rename.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediascan/internal/errs"
	"mediascan/internal/fingerprint"
)

// Layout names inside the checkpoint directory.
const (
	dirName   = "checkpoints"
	indexName = "index.json"
	storeName = "store.json"
	tmpSuffix = ".tmp"
)

// ChunkState is the recorded outcome for one chunk.
type ChunkState string

const (
	// StateCommitted means every object in the chunk passed validation.
	StateCommitted ChunkState = "committed"
	// StateFailed means at least one object errored; its index was skipped.
	StateFailed ChunkState = "failed"
)

// Chunk is one checkpoint entry.
type Chunk struct {
	ID           int        `json:"id"`
	InputHash    string     `json:"input_hash"`
	ManifestHash string     `json:"manifest_hash"`
	RuleVersion  string     `json:"rule_version"`
	State        ChunkState `json:"state"`
	// SegmentHashes are the content hashes produced by committed objects.
	SegmentHashes []string `json:"segment_hashes,omitempty"`
	// ErrorCount is the number of structured errors recorded in the chunk.
	ErrorCount int `json:"error_count"`
}

// Identity groups the three values that must match for reuse.
type Identity struct {
	InputHash    string
	ManifestHash string
	RuleVersion  string
}

// Matches reports whether a stored chunk may be reused for this run.
func (c Chunk) Matches(id Identity) bool {
	return c.State == StateCommitted &&
		c.InputHash == id.InputHash &&
		c.ManifestHash == id.ManifestHash &&
		c.RuleVersion == id.RuleVersion
}

// Store is the on-disk checkpoint store plus the serialized index.
type Store struct {
	root  string
	store storeFile
}

type storeFile struct {
	Chunks []Chunk `json:"chunks"`
}

// IndexSnapshot is the local index written atomically next to the store.
type IndexSnapshot struct {
	RuleVersion string               `json:"rule_version"`
	Records     []fingerprint.Record `json:"records"`
	Keyframes   []KeyframeDTO        `json:"keyframes"`
}

// KeyframeDTO is the serialized keyframe shape (avoids an import cycle by
// mirroring index.Keyframe's JSON tags here).
type KeyframeDTO struct {
	TrackID      uint32 `json:"track_id"`
	SampleNumber uint32 `json:"sample_number"`
	SegmentIndex int64  `json:"segment_index"`
	TimeTicks    int64  `json:"time_ticks"`
	ByteOffset   int64  `json:"byte_offset"`
	State        string `json:"state"`
}

// Open loads or creates a checkpoint store under outDir.
func Open(outDir string) (*Store, error) {
	root := filepath.Join(outDir, dirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, errs.New(errs.KindCheckpoint, root, errs.Unknown,
			errs.Unknown, 0, "cannot create checkpoint dir: %v", err)
	}
	s := &Store{root: root}
	path := filepath.Join(root, storeName)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if jerr := json.Unmarshal(raw, &s.store); jerr != nil {
			return nil, errs.New(errs.KindCheckpoint, path, 0, errs.Unknown, 0,
				"checkpoint store is corrupt: %v", jerr)
		}
	case errors.Is(err, os.ErrNotExist):
		// First run: empty store.
	default:
		return nil, errs.New(errs.KindCheckpoint, path, errs.Unknown,
			errs.Unknown, 0, "cannot read store: %v", err)
	}
	return s, nil
}

// Reusable returns the committed chunk for id when identity matches, else
// nil so the caller redoes the chunk from scratch.
func (s *Store) Reusable(id int, identity Identity) *Chunk {
	for i := range s.store.Chunks {
		if s.store.Chunks[i].ID == id && s.store.Chunks[i].Matches(identity) {
			return &s.store.Chunks[i]
		}
	}
	return nil
}

// FirstIncomplete returns the smallest chunk id without a committed,
// identity-matching entry, or -1 when every expected chunk is present.
func (s *Store) FirstIncomplete(count int, identity Identity) int {
	for id := 0; id < count; id++ {
		if s.Reusable(id, identity) == nil {
			return id
		}
	}
	return -1
}

// Commit inserts or replaces one chunk and persists the whole store
// atomically. A failed chunk is recorded too, but never marked reusable.
func (s *Store) Commit(c Chunk) error {
	if c.RuleVersion == "" {
		return errs.New(errs.KindCheckpoint, s.root, errs.Unknown,
			int64(c.ID), 0, "chunk %d missing rule version", c.ID)
	}
	replaced := false
	for i := range s.store.Chunks {
		if s.store.Chunks[i].ID == c.ID {
			s.store.Chunks[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		s.store.Chunks = append(s.store.Chunks, c)
	}
	sort.SliceStable(s.store.Chunks, func(i, j int) bool {
		return s.store.Chunks[i].ID < s.store.Chunks[j].ID
	})
	return s.persistStore()
}

func (s *Store) persistStore() error {
	raw, err := json.MarshalIndent(s.store, "", "  ")
	if err != nil {
		return errs.New(errs.KindCheckpoint, s.root, errs.Unknown,
			errs.Unknown, 0, "store marshal failed: %v", err)
	}
	return atomicWrite(filepath.Join(s.root, storeName), raw)
}

// WriteIndex atomically replaces the local index snapshot.
func (s *Store) WriteIndex(snap IndexSnapshot) error {
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return errs.New(errs.KindCheckpoint, s.root, errs.Unknown,
			errs.Unknown, 0, "index marshal failed: %v", err)
	}
	return atomicWrite(filepath.Join(s.root, indexName), raw)
}

// ReadIndex loads the local index for the verify command.
func (s *Store) ReadIndex() (*IndexSnapshot, error) {
	path := filepath.Join(s.root, indexName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.New(errs.KindCheckpoint, path, errs.Unknown,
			errs.Unknown, 0, "index not found: run a scan first")
	}
	var snap IndexSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, errs.New(errs.KindCheckpoint, path, 0, errs.Unknown, 0,
			"index file is corrupt: %v", err)
	}
	if snap.RuleVersion == "" {
		return nil, errs.New(errs.KindArgs, path, 0, errs.Unknown, 0,
			"index missing rule version")
	}
	return &snap, nil
}

// CommittedCount reports how many reusable, committed chunks are stored.
func (s *Store) CommittedCount() int {
	n := 0
	for _, c := range s.store.Chunks {
		if c.State == StateCommitted {
			n++
		}
	}
	return n
}

// ChunkCount reports total stored chunk entries (including failed).
func (s *Store) ChunkCount() int { return len(s.store.Chunks) }

// atomicWrite writes to a temp file in the same directory and renames it
// over the target, so a crash never leaves a half-written index.
func atomicWrite(path string, data []byte) error {
	tmp := path + tmpSuffix
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return errs.New(errs.KindCheckpoint, path, errs.Unknown,
			errs.Unknown, 0, "temp write failed: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return errs.New(errs.KindCheckpoint, path, errs.Unknown,
			errs.Unknown, 0, "atomic rename failed: %v", err)
	}
	return nil
}

// String summarizes the store for terminal output.
func (s *Store) String() string {
	return fmt.Sprintf("checkpoint(chunks=%d committed=%d)",
		len(s.store.Chunks), s.CommittedCount())
}
