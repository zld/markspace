// Package pipeline orchestrates processing stages with atomic per-stage
// checkpoints, resume validation and provenance recording.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"regexp"

	"mediapack/merr"
	"mediapack/safefs"
)

// Checkpoint is the atomic record of one completed stage. It binds the
// stage output to the input and parameter digests and to the previous
// stage's checkpoint, forming a chain.
type Checkpoint struct {
	Stage        string          `json:"stage"`
	InputDigest  string          `json:"input_digest"`
	ParamsDigest string          `json:"params_digest"`
	PrevDigest   string          `json:"prev_digest"`
	ResultDigest string          `json:"result_digest"`
	Payload      json.RawMessage `json:"payload"`
	Digest       string          `json:"digest"`
}

// ComputeDigest derives the checkpoint digest over every field that
// guards reuse. Deterministic: identical inputs and parameters reproduce
// identical digests.
func (c *Checkpoint) ComputeDigest() string {
	h := sha256.New()
	for _, s := range []string{c.Stage, c.InputDigest, c.ParamsDigest, c.PrevDigest, c.ResultDigest} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	h.Write(c.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// ResultDigestOf hashes a stage payload.
func ResultDigestOf(payload json.RawMessage) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Verify checks the checkpoint's internal consistency.
func (c *Checkpoint) Verify() error {
	if c.Digest != c.ComputeDigest() {
		return merr.Resumef("checkpoint", "stage %s: digest mismatch", c.Stage)
	}
	if c.ResultDigest != ResultDigestOf(c.Payload) {
		return merr.Resumef("checkpoint", "stage %s: payload digest mismatch", c.Stage)
	}
	return nil
}

var stageNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Store persists checkpoints inside the output directory.
type Store struct {
	Dir string // absolute or relative path to the checkpoint directory
}

// NewStore creates the checkpoint directory.
func NewStore(dir string) (*Store, error) {
	if _, err := safefs.WriteFileAtomic(dir, ".keep", nil, 0o644); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

// ErrNotFound marks a missing checkpoint.
var ErrNotFound = fs.ErrNotExist

// Save atomically writes a checkpoint (temp file + rename).
func (s *Store) Save(cp *Checkpoint) error {
	if !stageNameRe.MatchString(cp.Stage) {
		return merr.Paramf("checkpoint", "invalid stage name %q", cp.Stage)
	}
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	_, err = safefs.WriteFileAtomic(s.Dir, cp.Stage+".json", b, 0o644)
	return err
}

// Load reads and verifies a checkpoint. A corrupt checkpoint is reported
// as a resume error so the caller can fall back to the previous stage.
func (s *Store) Load(stage string) (*Checkpoint, error) {
	if !stageNameRe.MatchString(stage) {
		return nil, merr.Paramf("checkpoint", "invalid stage name %q", stage)
	}
	p, err := safefs.Join(s.Dir, stage+".json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, merr.Wrap(merr.KindResume, "checkpoint", "", "read stage "+stage, err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, merr.Wrap(merr.KindResume, "checkpoint", "", "corrupt checkpoint "+stage, err)
	}
	if cp.Stage != stage {
		return nil, merr.Resumef("checkpoint", "stage field %q does not match file %q", cp.Stage, stage)
	}
	if err := cp.Verify(); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Remove deletes checkpoints for the given stages (used to discard
// artifacts downstream of a corrupt or stale checkpoint).
func (s *Store) Remove(stages ...string) error {
	for _, st := range stages {
		if !stageNameRe.MatchString(st) {
			return merr.Paramf("checkpoint", "invalid stage name %q", st)
		}
		p, err := safefs.Join(s.Dir, st+".json")
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
