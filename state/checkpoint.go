// Package state persists deterministic resume points and validates them before
// an existing checkpoint is replaced.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"example.com/media-kernel/index"
	"example.com/media-kernel/media"
	"example.com/media-kernel/provenance"
)

// SchemaVersion versions the on-disk checkpoint format.
const SchemaVersion = "checkpoint-v1"

// Stage is a monotonic pipeline stage.
type Stage string

const (
	StageGenerated Stage = "generated"
	StageIngested  Stage = "ingested"
	StageAnalyzed  Stage = "analyzed"
	StageIndexed   Stage = "indexed"
	StageScanned   Stage = "scanned"
	StageSegments  Stage = "segments"
	StageCompleted Stage = "completed"
)

// SegmentState is the minimum data needed to skip a completed safe segment.
type SegmentState struct {
	Index     int    `json:"index"`
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	EndOffset int64  `json:"end_offset"`
	Keyframe  uint32 `json:"keyframe"`
}

// Checkpoint is the durable resume record.
type Checkpoint struct {
	SchemaVersion    string               `json:"schema_version"`
	InputHash        string               `json:"input_hash"`
	ParametersHash   string               `json:"parameters_hash"`
	Stage            Stage                `json:"stage"`
	Offset           int64                `json:"offset"`
	ExtendedSequence uint64               `json:"extended_sequence,omitempty"`
	IndexVersion     string               `json:"index_version"`
	SafeSegmentIndex int                  `json:"safe_segment_index"`
	Segments         map[int]SegmentState `json:"segments,omitempty"`
	Checksum         string               `json:"checksum"`
}

// Store reads and atomically writes one checkpoint file.
type Store struct {
	Path string
}

// NewCheckpoint constructs an initialized checkpoint.
func NewCheckpoint(inputHash, parametersHash string) *Checkpoint {
	return &Checkpoint{
		SchemaVersion:  SchemaVersion,
		InputHash:      inputHash,
		ParametersHash: parametersHash,
		Stage:          StageGenerated,
		IndexVersion:   index.IndexVersion,
		Segments:       map[int]SegmentState{},
	}
}

// Validate verifies format and exact input/parameter identity.
func (c *Checkpoint) Validate(inputHash, parametersHash string) error {
	if c == nil {
		return media.NewError("state", media.CodeCheckpoint, "nil checkpoint", nil)
	}
	if c.SchemaVersion != SchemaVersion {
		return media.NewError("state", media.CodeCheckpoint,
			fmt.Sprintf("schema %q != %q", c.SchemaVersion, SchemaVersion), nil)
	}
	if c.Checksum == "" {
		return media.NewError("state", media.CodeCheckpoint, "checkpoint has no checksum", nil)
	}
	want, err := c.computeChecksum()
	if err != nil {
		return err
	}
	if c.Checksum != want {
		return media.NewError("state", media.CodeHashMismatch, "checkpoint checksum mismatch", nil)
	}
	if inputHash != "" && c.InputHash != inputHash {
		return media.NewError("state", media.CodeHashMismatch,
			"checkpoint input hash does not match current input", nil)
	}
	if parametersHash != "" && c.ParametersHash != parametersHash {
		return media.NewError("state", media.CodeHashMismatch,
			"checkpoint parameters hash does not match current parameters", nil)
	}
	if c.IndexVersion != index.IndexVersion {
		return media.NewError("state", media.CodeCheckpoint,
			fmt.Sprintf("index version %q != %q", c.IndexVersion, index.IndexVersion), nil)
	}
	return nil
}

// RecordSegment stores a completed segment and advances the contiguous safe
// boundary only when segments 0..n are all present.
func (c *Checkpoint) RecordSegment(seg SegmentState) error {
	if c.Segments == nil {
		c.Segments = map[int]SegmentState{}
	}
	if existing, ok := c.Segments[seg.Index]; ok && existing.SHA256 != seg.SHA256 {
		return media.NewError("state", media.CodeHashMismatch,
			fmt.Sprintf("segment %d hash changed: checkpoint=%s current=%s",
				seg.Index, existing.SHA256, seg.SHA256), nil)
	}
	c.Segments[seg.Index] = seg
	for {
		if _, ok := c.Segments[c.SafeSegmentIndex]; !ok {
			break
		}
		c.SafeSegmentIndex++
	}
	c.Offset = seg.EndOffset
	c.Stage = StageSegments
	return nil
}

// IsComplete reports whether segment index has already been durably recorded.
func (c *Checkpoint) IsComplete(index int, currentHash string) bool {
	seg, ok := c.Segments[index]
	return ok && seg.SHA256 == currentHash
}

// Save validates and atomically replaces the checkpoint. Invalid checkpoints
// return before any rename, so an existing valid file is not overwritten.
func (s *Store) Save(c *Checkpoint) error {
	if s == nil || s.Path == "" {
		return media.NewError("state", media.CodeInvalidArgument, "checkpoint path is required", nil)
	}
	if c == nil {
		return media.NewError("state", media.CodeCheckpoint, "cannot save nil checkpoint", nil)
	}
	checksum, err := c.computeChecksum()
	if err != nil {
		return err
	}
	c.Checksum = checksum
	if err := c.Validate(c.InputHash, c.ParametersHash); err != nil {
		return err
	}
	b, err := provenance.CanonicalJSON(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return media.NewError("state", media.CodeCheckpoint, "cannot create checkpoint directory", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".checkpoint-*.tmp")
	if err != nil {
		return media.NewError("state", media.CodeCheckpoint, "cannot create temporary checkpoint", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return media.NewError("state", media.CodeCheckpoint, "cannot write checkpoint", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.NewError("state", media.CodeCheckpoint, "cannot sync checkpoint", err)
	}
	if err := tmp.Close(); err != nil {
		return media.NewError("state", media.CodeCheckpoint, "cannot close checkpoint", err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return media.NewError("state", media.CodeCheckpoint, "cannot replace checkpoint", err)
	}
	return nil
}

// SaveChecked verifies the proposed checkpoint against the expected input and
// parameter identities before touching the destination file.
func (s *Store) SaveChecked(c *Checkpoint, inputHash, parametersHash string) error {
	if c == nil {
		return media.NewError("state", media.CodeCheckpoint, "cannot save nil checkpoint", nil)
	}
	if inputHash != "" && c.InputHash != inputHash {
		return media.NewError("state", media.CodeHashMismatch,
			"refusing checkpoint: input identity does not match run", nil)
	}
	if parametersHash != "" && c.ParametersHash != parametersHash {
		return media.NewError("state", media.CodeHashMismatch,
			"refusing checkpoint: parameter identity does not match run", nil)
	}
	return s.Save(c)
}

// Load reads and validates the checkpoint checksum.
func (s *Store) Load() (*Checkpoint, error) {
	b, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, media.NewError("state", media.CodeCheckpoint, "cannot read checkpoint", err)
	}
	var c Checkpoint
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, media.NewError("state", media.CodeCheckpoint, "cannot parse checkpoint", err)
	}
	if err := c.Validate("", ""); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Checkpoint) computeChecksum() (string, error) {
	cp := *c
	cp.Checksum = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return "", media.NewError("state", media.CodeCheckpoint, "cannot checksum checkpoint", err)
	}
	return provenance.HashBytes(b), nil
}
