// Package index is the local artifact index. It records what has been
// published, keyed by dedup key, and is written atomically so a crash can never
// leave a half-written index. The batch layer writes artifacts first and then
// commits index entries; recovery reconciles the two on startup.
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Entry is one published artifact.
type Entry struct {
	Key            string     `json:"key"`
	ContentSHA     string     `json:"contentSha"`
	Size           int64      `json:"size"`
	Start          clock.Time `json:"start"`
	End            clock.Time `json:"end"`
	Manifest       string     `json:"manifest"`
	Representation string     `json:"representation"`
	Segment        string     `json:"segment"`
	// Artifact is the relative path under the artifact root.
	Artifact string `json:"artifact"`
	// Chunk is the batch chunk that published it.
	Chunk int `json:"chunk"`
	// PublishedAt is informational only and excluded from canonical digests.
	PublishedAt string `json:"publishedAt"`
}

// ChunkState is the committed state of one batch chunk.
type ChunkState struct {
	Chunk int `json:"chunk"`
	// InputDigest changes whenever any input segment in the chunk changes.
	InputDigest string   `json:"inputDigest"`
	Keys        []string `json:"keys"`
	Committed   bool     `json:"committed"`
}

// Store is the persisted index.
type Store struct {
	Version int          `json:"version"`
	Entries []Entry      `json:"entries"`
	Chunks  []ChunkState `json:"chunks"`
	// Artifacts is the artifact root relative to the work dir.
	Artifacts string `json:"artifacts"`
}

// ErrCorrupt indicates a damaged or unreadable index.
var ErrCorrupt = errors.New("index corrupt")

const currentVersion = 1

// Open loads the index from dir, creating an empty one if it does not exist.
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, "index.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{Version: currentVersion, Artifacts: "artifacts"}, nil
	}
	if err != nil {
		return nil, fault.NewErr(fault.CodeIndexCorrupt, fault.Locator{File: path}, "read index: %v", err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fault.NewErr(fault.CodeIndexCorrupt, fault.Locator{File: path, Offset: 0}, "decode index: %v", err)
	}
	if s.Version != currentVersion {
		return nil, fault.NewErr(fault.CodeIndexCorrupt, fault.Locator{File: path},
			"unsupported index version %d", s.Version)
	}
	if s.Artifacts == "" {
		s.Artifacts = "artifacts"
	}
	if err := s.validate(); err != nil {
		return nil, &fault.Err{Code: fault.CodeIndexCorrupt, Loc: fault.Locator{File: path}, Msg: err.Error()}
	}
	return &s, nil
}

func (s *Store) validate() error {
	seen := map[string]bool{}
	for i, e := range s.Entries {
		if e.Key == "" {
			return fmt.Errorf("entry %d has no key", i)
		}
		if seen[e.Key] {
			return fmt.Errorf("duplicate key %q", e.Key)
		}
		seen[e.Key] = true
		if e.Artifact == "" {
			return fmt.Errorf("entry %q has no artifact", e.Key)
		}
	}
	for i, c := range s.Chunks {
		if c.Chunk != i {
			return fmt.Errorf("chunk %d stored at position %d", c.Chunk, i)
		}
	}
	return nil
}

// Save writes the index atomically via a temp file and rename.
func (s *Store) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.NewErr(fault.CodeIndexCommit, fault.Locator{File: dir}, "create work dir: %v", err)
	}
	path := filepath.Join(dir, "index.json")
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fault.NewErr(fault.CodeIndexCommit, fault.Locator{File: path}, "encode index: %v", err)
	}
	if err := writeAtomic(path, data); err != nil {
		return fault.NewErr(fault.CodeIndexCommit, fault.Locator{File: path}, "write index: %v", err)
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Lookup returns the entry for key.
func (s *Store) Lookup(key string) (Entry, bool) {
	for _, e := range s.Entries {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// Put inserts or replaces an entry.
func (s *Store) Put(e Entry) {
	if e.PublishedAt == "" {
		e.PublishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	for i := range s.Entries {
		if s.Entries[i].Key == e.Key {
			s.Entries[i] = e
			return
		}
	}
	s.Entries = append(s.Entries, e)
}

// Chunk returns the committed state for a chunk number.
func (s *Store) Chunk(n int) (ChunkState, bool) {
	for _, c := range s.Chunks {
		if c.Chunk == n {
			return c, true
		}
	}
	return ChunkState{}, false
}

// CommitChunk marks a chunk committed and records its keys.
func (s *Store) CommitChunk(n int, digest string, keys []string) {
	cs := ChunkState{Chunk: n, InputDigest: digest, Keys: append([]string(nil), keys...), Committed: true}
	sort.Strings(cs.Keys)
	for i := range s.Chunks {
		if s.Chunks[i].Chunk == n {
			s.Chunks[i] = cs
			return
		}
	}
	s.Chunks = append(s.Chunks, cs)
	sort.Slice(s.Chunks, func(i, j int) bool { return s.Chunks[i].Chunk < s.Chunks[j].Chunk })
}

// MaxCommittedChunk returns the highest contiguous committed chunk prefix.
func (s *Store) MaxCommittedChunk() int {
	for i, c := range s.Chunks {
		if c.Chunk != i || !c.Committed {
			return i
		}
	}
	return len(s.Chunks)
}

// KeysForChunk lists the keys a committed chunk recorded.
func (s *Store) KeysForChunk(n int) []string {
	if c, ok := s.Chunk(n); ok {
		return c.Keys
	}
	return nil
}

// SetKeysForChunk records keys for a chunk without marking it committed. Used
// when recovery adopts artifacts that were written before a crash.
func (s *Store) SetKeysForChunk(n int, digest string, keys []string) {
	for i := range s.Chunks {
		if s.Chunks[i].Chunk == n {
			s.Chunks[i].Keys = append([]string(nil), keys...)
			s.Chunks[i].InputDigest = digest
			s.Chunks[i].Committed = false
			return
		}
	}
	s.Chunks = append(s.Chunks, ChunkState{Chunk: n, InputDigest: digest, Keys: append([]string(nil), keys...)})
	sort.Slice(s.Chunks, func(i, j int) bool { return s.Chunks[i].Chunk < s.Chunks[j].Chunk })
}

// ArtifactPath joins the artifact root to a relative artifact name.
func (s *Store) ArtifactPath(workDir, artifact string) string {
	return filepath.Join(workDir, filepath.FromSlash(s.Artifacts), filepath.FromSlash(artifact))
}
