package dag

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"mediad/internal/model"
)

// SegmentCommit is the durable record of one finalized segment. The Key is
// stable across retries (representation + segment index), which makes
// CommitSegment idempotent: re-running a finished task never submits twice.
type SegmentCommit struct {
	Key      string `json:"key"`
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	ByteSize int64  `json:"byte_size"`
	Index    int    `json:"index"`
	Rep      string `json:"rep"`
}

// data is the on-disk checkpoint layout. Maps keep per-node progress; the
// outputs ledger is the authoritative set of already-submitted segments.
type data struct {
	Version      int                     `json:"version"`
	Generation   int64                   `json:"generation"`
	Nodes        map[string]State        `json:"nodes"`
	InputOffsets map[string]int64        `json:"input_offsets"`
	Outputs      map[string]SegmentCommit `json:"outputs"`
}

// Checkpoint is a mutex-guarded, atomically persisted progress store.
type Checkpoint struct {
	mu   sync.RWMutex
	path string
	d    data
}

// TaskKey builds the stable key for one representation/segment pair.
func TaskKey(rep string, idx int) string { return fmt.Sprintf("%s:%d", rep, idx) }

// LoadCheckpoint reads a checkpoint. A missing file yields an empty store
// plus a sentinel-wrapped error so callers can distinguish first run from
// resume without treating absence as a failure.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	cp := &Checkpoint{path: path, d: data{
		Version:      1,
		Nodes:        map[string]State{},
		InputOffsets: map[string]int64{},
		Outputs:      map[string]SegmentCommit{},
	}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cp, model.ErrNotFound
	}
	if err != nil {
		return cp, err
	}
	if err := json.Unmarshal(raw, &cp.d); err != nil {
		return nil, fmt.Errorf("dag: invalid checkpoint at %s: %w", path, err)
	}
	if cp.d.Nodes == nil || cp.d.Outputs == nil || cp.d.InputOffsets == nil {
		return nil, fmt.Errorf("dag: incomplete checkpoint at %s", path)
	}
	return cp, nil
}

// Path returns the checkpoint file location.
func (c *Checkpoint) Path() string { return c.path }

// MarkNode records a node state and durably persists the whole checkpoint.
func (c *Checkpoint) MarkNode(key string, st State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.d.Nodes[key] = st
	return c.saveLocked()
}

// NodeState returns a persisted node state.
func (c *Checkpoint) NodeState(key string) (State, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.d.Nodes[key]
	return st, ok
}

// SetInputOffset persists how far a node has consumed its input.
func (c *Checkpoint) SetInputOffset(node string, offset int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.d.InputOffsets[node] = offset
	return c.saveLocked()
}

// InputOffset returns a persisted input offset.
func (c *Checkpoint) InputOffset(node string) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	off, ok := c.d.InputOffsets[node]
	return off, ok
}

// CommitSegment adds a segment to the ledger. It is idempotent: a key that
// was committed before returns the existing record and false, so retried
// tasks cannot double-submit. New commits return true.
func (c *Checkpoint) CommitSegment(commit SegmentCommit) (SegmentCommit, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.d.Outputs[commit.Key]; ok {
		return existing, false, nil
	}
	c.d.Outputs[commit.Key] = commit
	if err := c.saveLocked(); err != nil {
		delete(c.d.Outputs, commit.Key)
		return SegmentCommit{}, false, err
	}
	return commit, true, nil
}

// IsCommitted reports whether a stable task key is already in the ledger.
func (c *Checkpoint) IsCommitted(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.d.Outputs[key]
	return ok
}

// Commits returns all ledger entries sorted by (index, representation).
func (c *Checkpoint) Commits() []SegmentCommit {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]SegmentCommit, 0, len(c.d.Outputs))
	for _, v := range c.d.Outputs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		return out[i].Rep < out[j].Rep
	})
	return out
}

// Generation increments on every successful save, giving resume code a
// monotonic "checkpoint version" to report.
func (c *Checkpoint) Generation() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.d.Generation
}

// saveLocked writes to a sibling temp file, fsyncs and atomically renames.
// Callers must hold the write lock.
func (c *Checkpoint) saveLocked() error {
	c.d.Generation++
	raw, err := json.MarshalIndent(&c.d, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".tmp-checkpoint.json")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
