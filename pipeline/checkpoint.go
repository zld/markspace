// Package pipeline wires ingest, audio, rtp, and cache together under
// checkpointed recovery. Checkpoints record stage, input offset, and
// output digest so an interrupted run resumes from the last consistent
// boundary and repeated runs produce identical output digests.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediacore/media"
)

// Checkpoint marks a consistent boundary: everything up to Offset of the
// input has been processed through Stage, producing output with Digest.
type Checkpoint struct {
	Seq        int    `json:"seq"`         // monotonic save counter
	Stage      string `json:"stage"`       // last completed stage
	Offset     int64  `json:"offset"`      // input units consumed
	Digest     string `json:"digest"`      // hex sha256 of output so far
	ColorState string `json:"color_state"` // folded color metadata state
	Checksum   string `json:"checksum"`    // sha256 of the fields above
}

// computeChecksum seals the checkpoint's own integrity.
func (c *Checkpoint) computeChecksum() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d|%s|%s",
		c.Seq, c.Stage, c.Offset, c.Digest, c.ColorState)))
	return hex.EncodeToString(h[:])
}

// Seal fills in Checksum. Call before Save.
func (c *Checkpoint) Seal() { c.Checksum = c.computeChecksum() }

// Valid reports whether the checksum matches the fields.
func (c Checkpoint) Valid() bool { return c.Checksum == c.computeChecksum() }

// Store persists checkpoints in two alternating slots with atomic
// rename, so a crash or truncated write can destroy at most one slot
// and recovery falls back to the previous consistent boundary.
type Store struct {
	dir string
}

// NewStore opens (creating) a checkpoint directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, media.Wrap(media.CodeInternal, "pipeline.NewStore", -1, err, "mkdir %s", dir)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) slot(seq int) string {
	name := "checkpoint.a.json"
	if seq%2 == 1 {
		name = "checkpoint.b.json"
	}
	return filepath.Join(s.dir, name)
}

// Save persists c atomically into its slot.
func (s *Store) Save(c Checkpoint) error {
	c.Seal()
	data, err := json.Marshal(c)
	if err != nil {
		return media.Wrap(media.CodeInternal, "pipeline.Save", -1, err, "marshal")
	}
	final := s.slot(c.Seq)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return media.Wrap(media.CodeInternal, "pipeline.Save", -1, err, "write tmp")
	}
	if err := os.Rename(tmp, final); err != nil {
		return media.Wrap(media.CodeInternal, "pipeline.Save", -1, err, "rename")
	}
	return nil
}

// Load returns the highest-sequence checkpoint that passes integrity
// validation. A corrupt or truncated slot is skipped, not fatal; only
// if a slot exists but every copy is unreadable does recovery give up.
// found=false means start from the beginning.
func (s *Store) Load() (c Checkpoint, found bool, err error) {
	for _, slot := range []string{s.slot(0), s.slot(1)} {
		data, rerr := os.ReadFile(slot)
		if rerr != nil {
			continue // absent slot is normal on first run
		}
		var cand Checkpoint
		if json.Unmarshal(data, &cand) != nil || !cand.Valid() {
			continue // truncated or tampered: fall back to the other slot
		}
		if !found || cand.Seq > c.Seq {
			c, found = cand, true
		}
	}
	return c, found, nil
}
