// Package batch manages chunked processing state and crash-safe resume.
// A chunk is only marked committed after its output file and index record
// are both fully durable; a checkpoint never points at partially written
// data. On resume, committed chunks are verified and skipped, and any
// output/index mismatch aborts with the first inconsistent chunk id.
package batch

import (
	"os"
	"path/filepath"

	merr "mediatool/internal/errors"
	"mediatool/internal/index"
)

// Status enumerates chunk lifecycle states persisted in the checkpoint.
type Status string

const (
	StatusPending   Status = "pending"
	StatusCommitted Status = "committed"
	StatusFailed    Status = "failed"
)

// ChunkState is one entry of the checkpoint.
type ChunkState struct {
	ID         int    `json:"id"`
	Status     Status `json:"status"`
	OutputPath string `json:"output_path"`
	OutputSHA  string `json:"output_sha"`
	InputSHA   string `json:"input_sha"`
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
}

// Checkpoint is the on-disk chunk-state store plus the linked index.
type Checkpoint struct {
	dir    string
	path   string
	states []ChunkState
	idx    *index.Index
}

// New creates (or opens) a checkpoint inside dir, describing total chunks.
func New(dir string, total int) (*Checkpoint, error) {
	if err := os.MkdirAll(filepath.Join(dir, "chunks"), 0o755); err != nil {
		return nil, err
	}
	idx, err := index.Open(dir)
	if err != nil {
		return nil, merr.New(merr.CodeCheckpointCorrupt, "open index: %v", err)
	}
	cp := &Checkpoint{
		dir:    dir,
		path:   filepath.Join(dir, "checkpoint.json"),
		states: make([]ChunkState, total),
		idx:    idx,
	}
	for i := range cp.states {
		cp.states[i] = ChunkState{ID: i, Status: StatusPending}
	}
	if err := cp.load(); err != nil {
		return nil, err
	}
	return cp, nil
}

// Index exposes the underlying record store.
func (c *Checkpoint) Index() *index.Index { return c.idx }

// PendingChunks returns chunk ids that still need processing, in order.
func (c *Checkpoint) PendingChunks() []int {
	out := make([]int, 0, len(c.states))
	for _, s := range c.states {
		if s.Status != StatusCommitted {
			out = append(out, s.ID)
		}
	}
	return out
}

// CommittedCount returns how many chunks are durably committed.
func (c *Checkpoint) CommittedCount() int {
	n := 0
	for _, s := range c.states {
		if s.Status == StatusCommitted {
			n++
		}
	}
	return n
}

// MarkFailed records a failure without touching committed state.
func (c *Checkpoint) MarkFailed(id int) error {
	if id < 0 || id >= len(c.states) {
		return merr.New(merr.CodeCheckpointCorrupt, "chunk id %d out of range", id)
	}
	c.states[id].Status = StatusFailed
	return c.persist()
}

// Commit durably records a finished chunk: the output must already exist and
// match wantSHA. The index record is committed first (atomic rename), then
// the checkpoint is atomically updated. A crash between the two leaves the
// index authoritative; Verify reconciles on the next run.
func (c *Checkpoint) Commit(rec index.Record, wantSHA string) error {
	id := rec.ChunkID
	if id < 0 || id >= len(c.states) {
		return merr.New(merr.CodeCheckpointCorrupt, "chunk id %d out of range", id)
	}
	outPath := filepath.Join(c.dir, rec.OutputPath)
	if err := verifyFileSHA(outPath, wantSHA); err != nil {
		return err
	}
	rec.OutputSHA256 = wantSHA
	if err := c.idx.Commit(rec); err != nil {
		return merr.New(merr.CodeCheckpointCorrupt, "index commit: %v", err)
	}
	c.states[id] = ChunkState{
		ID: id, Status: StatusCommitted, OutputPath: rec.OutputPath,
		OutputSHA: wantSHA, InputSHA: rec.InputDigest,
		StartNanos: rec.StartNanos, EndNanos: rec.EndNanos,
	}
	if err := c.persist(); err != nil {
		return merr.New(merr.CodeCheckpointCorrupt, "checkpoint persist: %v", err)
	}
	return nil
}

// Verify cross-checks every committed chunk against the index and the actual
// output file. It returns an index_mismatch error naming the first
// inconsistent chunk, so a resume refuses to run on diverged state.
func (c *Checkpoint) Verify() error {
	for i := range c.states {
		s := c.states[i]
		rec, inIndex := c.idx.Get(i)
		switch {
		case s.Status == StatusCommitted && !inIndex:
			return merr.At(merr.CodeIndexMismatch, int64(i),
				"chunk %d committed in checkpoint but absent from index", i)
		case s.Status != StatusCommitted && inIndex:
			return merr.At(merr.CodeIndexMismatch, int64(i),
				"chunk %d in index but not committed in checkpoint", i)
		case s.Status != StatusCommitted:
			continue
		}
		if rec.OutputSHA256 != s.OutputSHA {
			return merr.At(merr.CodeIndexMismatch, int64(i),
				"chunk %d index SHA %s != checkpoint SHA %s",
				i, rec.OutputSHA256, s.OutputSHA)
		}
		if err := verifyFileSHA(filepath.Join(c.dir, s.OutputPath), s.OutputSHA); err != nil {
			return merr.At(merr.CodeChunkIncomplete, int64(i),
				"chunk %d output verification: %v", i, err)
		}
	}
	return nil
}
