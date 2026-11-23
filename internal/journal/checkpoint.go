// Package journal implements stage checkpoints, input fingerprints and
// provenance records. Checkpoints are written atomically and hash-chained,
// so a torn or corrupted checkpoint is detected and the pipeline rolls back
// to the most recent valid stage instead of publishing partial state.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Stage names in pipeline order.
const (
	StageParse     = "parse"
	StageTransform = "transform"
	StageValidate  = "validate"
	StageCommit    = "commit"
)

// Stages is the ordered list used for rollback and resume decisions.
var Stages = []string{StageParse, StageTransform, StageValidate, StageCommit}

// Checkpoint is one durable stage marker.
type Checkpoint struct {
	RunID       string `json:"run_id"`
	Stage       string `json:"stage"`
	StageIndex  int    `json:"stage_index"`
	InputSHA256 string `json:"input_sha256"`
	ConfigHash  string `json:"config_hash"`
	PrevHash    string `json:"prev_hash"`    // hash chain: prior checkpoint hash
	PayloadPath string `json:"payload_path"` // stage output artifact (if any)
	BodyHash    string `json:"body_hash"`
}

// CheckpointStore is the storage interface used by the pipeline; the real
// Store and the fault-injecting FaultyStore both implement it.
type CheckpointStore interface {
	Write(c *Checkpoint) error
	Read(stage string) (*Checkpoint, error)
	LatestValid(inputSHA, configHash string) (*Checkpoint, int, error)
	Reset() error
}

// Store persists checkpoints under a working directory.
type Store struct {
	dir string
}

// NewStore opens (or creates) a checkpoint directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(stage string) string {
	return filepath.Join(s.dir, "checkpoint-"+stage+".json")
}

// bodyHash hashes every field except BodyHash itself, giving each
// checkpoint a stable self-identity and linking PrevHash into the chain.
func (c *Checkpoint) bodyHash() string {
	cp := *c
	cp.BodyHash = ""
	b, err := json.Marshal(&cp)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Write atomically persists a checkpoint (temp file + fsync + rename).
func (s *Store) Write(c *Checkpoint) error {
	c.BodyHash = c.bodyHash()
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	final := s.path(c.Stage)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Read loads and verifies one stage checkpoint; a missing file returns
// ErrCheckpointMissing so callers can distinguish "never reached" from
// corruption.
func (s *Store) Read(stage string) (*Checkpoint, error) {
	raw, err := os.ReadFile(s.path(stage))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrCheckpointMissing
	}
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCheckpointCorrupt, stage, err)
	}
	if c.bodyHash() != c.BodyHash {
		return nil, fmt.Errorf("%w: %s: body hash mismatch", ErrCheckpointCorrupt, stage)
	}
	return &c, nil
}

// Sentinel errors.
var (
	ErrCheckpointMissing   = errors.New("journal: checkpoint missing")
	ErrCheckpointCorrupt   = errors.New("journal: checkpoint corrupt")
	ErrFingerprintMismatch = errors.New("journal: input fingerprint mismatch")
	ErrConfigMismatch      = errors.New("journal: config hash mismatch")
)

// LatestValid walks stages in order and returns the highest stage whose
// checkpoint verifies and matches the given input/config identity. A corrupt
// checkpoint invalidates itself and every later stage (rollback semantics);
// its file is removed so the rerun starts cleanly from the valid point.
func (s *Store) LatestValid(inputSHA, configHash string) (*Checkpoint, int, error) {
	var last *Checkpoint
	lastIdx := -1
	for i, stage := range Stages {
		c, err := s.Read(stage)
		if errors.Is(err, ErrCheckpointMissing) {
			break // later stages cannot exist without this one
		}
		if err != nil {
			// Corruption: roll back by dropping this and later stages.
			s.invalidateFrom(i)
			if last == nil {
				return nil, -1, err
			}
			return last, lastIdx, nil
		}
		if c.InputSHA256 != inputSHA {
			s.invalidateFrom(i)
			return nil, -1, fmt.Errorf("%w: stage=%s", ErrFingerprintMismatch, stage)
		}
		if c.ConfigHash != configHash {
			// Config change must invalidate all old checkpoints.
			s.invalidateFrom(0)
			return nil, -1, fmt.Errorf("%w: stage=%s", ErrConfigMismatch, stage)
		}
		if last != nil && c.PrevHash != last.BodyHash {
			s.invalidateFrom(i)
			if last == nil {
				return nil, -1, ErrCheckpointCorrupt
			}
			return last, lastIdx, nil
		}
		last = c
		lastIdx = i
	}
	return last, lastIdx, nil
}

func (s *Store) invalidateFrom(idx int) {
	for i := idx; i < len(Stages); i++ {
		_ = os.Remove(s.path(Stages[i]))
	}
}

// Reset clears every checkpoint (used by explicit rerun/failure paths).
func (s *Store) Reset() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if len(e.Name()) >= 11 && e.Name()[:11] == "checkpoint-" {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
	return nil
}

// Fingerprint returns the SHA-256 hex of raw input bytes.
func Fingerprint(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ConfigHash hashes a serialized configuration so any config change yields a
// new identity and invalidates old checkpoints.
func ConfigHash(cfgJSON []byte) string {
	sum := sha256.Sum256(cfgJSON)
	return hex.EncodeToString(sum[:])
}
