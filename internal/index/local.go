// Package index persists per-chunk processing records with atomic
// write-and-rename semantics. A reader never observes a partial record:
// data lands in a temporary file first and is committed by rename.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Record describes one committed chunk and everything needed to verify it
// on a later resume.
type Record struct {
	ChunkID      int    `json:"chunk_id"`
	InputDigest  string `json:"input_digest"`
	OutputPath   string `json:"output_path"`
	OutputSHA256 string `json:"output_sha256"`
	StartNanos   int64  `json:"start_nanos"`
	EndNanos     int64  `json:"end_nanos"`
	SampleCount  int    `json:"sample_count"`
}

// Index is the on-disk, mutex-guarded collection of chunk records.
type Index struct {
	mu      sync.RWMutex
	path    string
	records map[int]Record
}

// Open loads an existing index at path, or starts an empty one if no file
// exists yet.
func Open(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	idx := &Index{
		path:    filepath.Join(dir, "index.json"),
		records: make(map[int]Record),
	}
	data, err := os.ReadFile(idx.path)
	if errors.Is(err, fs.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Record
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		idx.records[r.ChunkID] = r
	}
	return idx, nil
}

// Commit writes the record to a temporary file and atomically renames it over
// the index. The caller is expected to have fully flushed the chunk output
// before calling Commit.
func (idx *Index) Commit(r Record) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	next := make(map[int]Record, len(idx.records)+1)
	for k, v := range idx.records {
		next[k] = v
	}
	next[r.ChunkID] = r

	if err := idx.writeAtomic(next); err != nil {
		return err
	}
	idx.records = next
	return nil
}

// writeAtomic serializes to <index>.tmp and renames. On POSIX, rename is
// atomic, so a crash leaves either the old complete index or the new one.
func (idx *Index) writeAtomic(recs map[int]Record) error {
	list := make([]Record, 0, len(recs))
	for _, r := range recs {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ChunkID < list[j].ChunkID })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := idx.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	// fsync guarantees the temp data is durable before the rename.
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	return os.Rename(tmp, idx.path)
}

// Get returns the record for a chunk.
func (idx *Index) Get(chunkID int) (Record, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	r, ok := idx.records[chunkID]
	return r, ok
}

// Records returns all records ordered by chunk id.
func (idx *Index) Records() []Record {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	list := make([]Record, 0, len(idx.records))
	for _, r := range idx.records {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ChunkID < list[j].ChunkID })
	return list
}

// Len returns the number of committed records.
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.records)
}

// Digest returns a stable SHA-256 over the canonical (chunk-ordered) record
// list. Two identical index contents always produce the same digest, which
// the resume path uses to prove repeatable runs.
func (idx *Index) Digest() string {
	list := idx.Records()
	data, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashBytes is a convenience SHA-256 helper for chunk output verification.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
