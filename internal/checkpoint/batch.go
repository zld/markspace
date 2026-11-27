// Package checkpoint splits a scan into fixed-size chunks and persists a
// checkpoint before and after each chunk. The invariant is: artifacts are
// written first, then the index is committed. A crash between the two leaves an
// artifact with no commit, which recovery adopts instead of republishing.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Chunk is a contiguous run of segments.
type Chunk struct {
	Index int `json:"index"`
	// First and Last are positions in the flat segment list, inclusive.
	First int `json:"first"`
	Last  int `json:"last"`
	// InputDigest covers every segment identity in the chunk.
	InputDigest string `json:"inputDigest"`
}

// Count returns the number of segments in the chunk.
func (c Chunk) Count() int { return c.Last - c.First + 1 }

// Plan splits total segments into chunks of at most size, in order.
func Plan(total, size int) ([]Chunk, error) {
	if size <= 0 {
		return nil, fault.NewErr(fault.CodeCheckpoint, fault.Locator{}, "chunk size must be positive, got %d", size)
	}
	if total < 0 {
		return nil, fault.NewErr(fault.CodeCheckpoint, fault.Locator{}, "negative segment count %d", total)
	}
	var out []Chunk
	for first := 0; first < total; first += size {
		last := first + size - 1
		if last >= total {
			last = total - 1
		}
		out = append(out, Chunk{Index: len(out), First: first, Last: last})
	}
	return out, nil
}

// Identity is the stable per-segment value a chunk digest is built from. It
// includes the source position and timing so any input change invalidates the
// chunk, forcing revalidation instead of stale reuse.
type Identity struct {
	Manifest       string     `json:"manifest"`
	Representation string     `json:"representation"`
	Segment        string     `json:"segment"`
	Start          clock.Time `json:"start"`
	End            clock.Time `json:"end"`
	Path           string     `json:"path"`
	// ContentHash is the validated segment digest. Including it means a file
	// replaced in place with the same name and timing still invalidates the
	// chunk, so changed input is always revalidated.
	ContentHash string `json:"contentHash"`
}

// Digest hashes a contiguous run of identities.
func Digest(ids []Identity) string {
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s\n",
			id.Manifest, id.Representation, id.Segment, id.Start, id.End, id.Path, id.ContentHash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AssignDigests fills each chunk's InputDigest from the flat identity list.
func AssignDigests(chunks []Chunk, ids []Identity) {
	for i := range chunks {
		lo, hi := chunks[i].First, chunks[i].Last+1
		if lo < 0 {
			lo = 0
		}
		if hi > len(ids) {
			hi = len(ids)
		}
		if lo > hi {
			lo = hi
		}
		chunks[i].InputDigest = Digest(ids[lo:hi])
	}
}

// State is the on-disk checkpoint document.
type State struct {
	Version int `json:"version"`
	// ChunkSize is the fixed chunk size for the whole run.
	ChunkSize int     `json:"chunkSize"`
	Chunks    []Chunk `json:"chunks"`
	// Completed is the number of leading chunks that finished cleanly.
	Completed int `json:"completed"`
	// CrashedChunk is the chunk that was in flight, or -1.
	CrashedChunk int `json:"crashedChunk"`
	// Running marks a checkpoint written before a chunk starts.
	Running bool `json:"running"`
}

const version = 1

// Path returns the checkpoint file location in a work dir.
func Path(workDir string) string { return filepath.Join(workDir, "checkpoint.json") }

// Load reads the checkpoint; a missing file yields a fresh state.
func Load(workDir string) (*State, error) {
	path := Path(workDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{Version: version, CrashedChunk: -1}, nil
	}
	if err != nil {
		return nil, fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path}, "read checkpoint: %v", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path, Offset: 0}, "decode checkpoint: %v", err)
	}
	if s.Version != version {
		return nil, fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path},
			"unsupported checkpoint version %d", s.Version)
	}
	if s.CrashedChunk == 0 && s.Completed == 0 && !s.Running {
		// A zero value from JSON decodes as -1 only if present; normalise a
		// missing crashedChunk to -1 so it never looks like a real crash.
		s.CrashedChunk = -1
	}
	return &s, nil
}

// Save writes the checkpoint atomically.
func (s *State) Save(workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: workDir}, "create work dir: %v", err)
	}
	path := Path(workDir)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path}, "encode checkpoint: %v", err)
	}
	tmp, err := os.CreateTemp(workDir, ".checkpoint-*.tmp")
	if err != nil {
		return fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path}, "temp checkpoint: %v", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path}, "write checkpoint: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path}, "sync checkpoint: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fault.NewErr(fault.CodeCheckpoint, fault.Locator{File: path}, "close checkpoint: %v", err)
	}
	return os.Rename(name, path)
}

// Begin records that a chunk is in flight.
func (s *State) Begin(chunk int) {
	s.Running = true
	s.CrashedChunk = chunk
}

// Finish records that a chunk completed cleanly.
func (s *State) Finish(chunk int) {
	s.Running = false
	s.CrashedChunk = -1
	if chunk+1 > s.Completed {
		s.Completed = chunk + 1
	}
}

// Summarize renders a compact human line for the resume report.
func (s *State) Summarize() string {
	var b strings.Builder
	fmt.Fprintf(&b, "checkpoint: chunk-size=%d chunks=%d completed=%d crashed=%d",
		s.ChunkSize, len(s.Chunks), s.Completed, s.CrashedChunk)
	return b.String()
}

// SortedIdentities is a small helper for deterministic identity lists.
func SortedIdentities(ids []Identity) []Identity {
	out := append([]Identity(nil), ids...)
	sort.SliceStable(out, func(i, j int) bool {
		if c := out[i].Start.Cmp(out[j].Start); c != 0 {
			return c < 0
		}
		if out[i].Manifest != out[j].Manifest {
			return out[i].Manifest < out[j].Manifest
		}
		if out[i].Representation != out[j].Representation {
			return out[i].Representation < out[j].Representation
		}
		return out[i].Segment < out[j].Segment
	})
	return out
}
