package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Checkpoint records durable progress so an interrupted run can resume without
// recomputing completed work. It is written atomically and never trusted
// blindly: completed segments are re-verified against their digests on load.
type Checkpoint struct {
	Version      int    `json:"version"`
	InputDigest  string `json:"input_digest"`
	ConfigDigest string `json:"config_digest"`
	// ParseOffset is the byte offset the parser reached when the checkpoint was
	// written. A resumed run with the same input digest may start here.
	ParseOffset int64 `json:"parse_offset"`
	// Completed lists finished segment indices and the digest each produced.
	Completed []CompletedSegment `json:"completed"`
	// Rejections records segments the aligner refused, with the reason. These
	// are resume points: the work is retried, not marked done.
	Rejections []Rejection `json:"rejections,omitempty"`
}

// CompletedSegment proves a finished segment's output digest.
type CompletedSegment struct {
	Index  int    `json:"index"`
	Digest string `json:"digest"`
}

// Rejection records a segment that could not be aligned losslessly.
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// ErrStaleCheckpoint reports a checkpoint that does not match the current input
// or config. Callers must start fresh rather than mixing incompatible work.
var ErrStaleCheckpoint = errors.New("pipeline: checkpoint does not match input or config")

// ErrCorruptCheckpoint reports a checkpoint whose completed digests no longer
// verify.
var ErrCorruptCheckpoint = errors.New("pipeline: checkpoint failed digest verification")

// NewCheckpoint creates an empty checkpoint for a run.
func NewCheckpoint(inputDigest, configDigest string) *Checkpoint {
	return &Checkpoint{Version: 1, InputDigest: inputDigest, ConfigDigest: configDigest}
}

// CompletedSet returns the set of completed segment indices.
func (c *Checkpoint) CompletedSet() map[int]string {
	out := make(map[int]string, len(c.Completed))
	for _, s := range c.Completed {
		out[s.Index] = s.Digest
	}
	return out
}

// MarkCompleted records a finished segment. Re-recording the same index with
// the same digest is a no-op; a different digest is a conflict and is refused
// so committed results cannot be silently rewritten.
func (c *Checkpoint) MarkCompleted(index int, digest string) error {
	for i := range c.Completed {
		if c.Completed[i].Index != index {
			continue
		}
		if c.Completed[i].Digest == digest {
			return nil
		}
		return fmt.Errorf("pipeline: segment %d already committed with a different digest", index)
	}
	c.Completed = append(c.Completed, CompletedSegment{Index: index, Digest: digest})
	c.sortCompleted()
	return nil
}

func (c *Checkpoint) sortCompleted() {
	sort.Slice(c.Completed, func(i, j int) bool { return c.Completed[i].Index < c.Completed[j].Index })
}

// VerifyCompleted re-derives each completed segment's digest with recompute and
// checks it matches the checkpoint. A mismatch means the checkpoint is stale or
// the artifact was corrupted, and the run must not resume from it.
func (c *Checkpoint) VerifyCompleted(recompute func(index int) (string, error)) error {
	for _, s := range c.Completed {
		got, err := recompute(s.Index)
		if err != nil {
			return fmt.Errorf("%w: segment %d: %v", ErrCorruptCheckpoint, s.Index, err)
		}
		if got != s.Digest {
			return fmt.Errorf("%w: segment %d digest %s != %s", ErrCorruptCheckpoint, s.Index, got, s.Digest)
		}
	}
	return nil
}

// Pending returns segment indices in [0,total) not completed, excluding
// rejected segments that are being retried only if retryRejected is false.
func (c *Checkpoint) Pending(total int) []int {
	done := c.CompletedSet()
	out := make([]int, 0, total)
	for i := 0; i < total; i++ {
		if _, ok := done[i]; !ok {
			out = append(out, i)
		}
	}
	return out
}

// RecomputedCount counts segments that will actually be recomputed given a
// total, i.e. pending work. It is the resume_recomputed metric's source.
func (c *Checkpoint) RecomputedCount(total int) int { return len(c.Pending(total)) }

// Save writes the checkpoint atomically as canonical JSON.
func (c *Checkpoint) Save(path string) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// LoadCheckpoint reads a checkpoint and checks it belongs to the run.
func LoadCheckpoint(path, inputDigest, configDigest string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("pipeline: corrupt checkpoint: %w", err)
	}
	if c.Version != 1 {
		return nil, fmt.Errorf("%w: version %d", ErrStaleCheckpoint, c.Version)
	}
	if c.InputDigest != inputDigest || c.ConfigDigest != configDigest {
		return nil, fmt.Errorf("%w: input/config digest differs", ErrStaleCheckpoint)
	}
	return &c, nil
}

// DigestBytes returns the hex SHA-256 of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
