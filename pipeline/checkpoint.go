// Package pipeline wires manifest parsing, analysis, caching and checkpoint
// recovery together. Every segment is committed to the checkpoint only after
// it has been fully analysed and accepted, so a resume can never observe a
// partial segment.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediamux/media"
)

// StageState is the coarse status of one pipeline stage.
type StageState string

const (
	StagePending  StageState = "pending"
	StageRunning  StageState = "running"
	StageComplete StageState = "complete"
	StageFailed   StageState = "failed"
)

// Checkpoint is the durable record that lets a resumed run continue from the
// last complete segment without redoing accepted work.
type Checkpoint struct {
	Version         int                   `json:"version"`
	ManifestDigest  string                `json:"manifest_digest"`
	ManifestKind    string                `json:"manifest_kind"`
	ManifestVersion int                   `json:"manifest_version"`
	InputDigest     string                `json:"input_digest"`
	Cursor          int                   `json:"cursor"` // next segment index to process
	LastCompleteIdx int                   `json:"last_complete_index"`
	CommittedBytes  int64                 `json:"committed_bytes"`
	ResultDigest    string                `json:"result_digest"`
	Stages          map[string]StageState `json:"stages"`
	SegmentDigests  map[int]string        `json:"segment_digests"`
	Epoch           int64                 `json:"epoch"`
}

// CheckpointVersion is the on-disk schema version.
const CheckpointVersion = 1

// NewCheckpoint starts a checkpoint for a fresh run.
func NewCheckpoint(kind string, manifestVersion int, manifestDigest, inputDigest string, epoch int64) *Checkpoint {
	return &Checkpoint{
		Version:         CheckpointVersion,
		ManifestKind:    kind,
		ManifestVersion: manifestVersion,
		ManifestDigest:  manifestDigest,
		InputDigest:     inputDigest,
		Cursor:          0,
		LastCompleteIdx: -1,
		Stages:          map[string]StageState{},
		SegmentDigests:  map[int]string{},
		Epoch:           epoch,
	}
}

// CommitSegment records a fully processed segment. It rejects going backwards
// so a resumed run cannot silently rewind its cursor.
func (c *Checkpoint) CommitSegment(idx int, digest string, bytes int64) error {
	if idx != c.Cursor {
		return media.NewError(media.CodeCursorBack, media.PhaseCheckpoint,
			fmt.Sprintf("segment %d cannot commit at cursor %d", idx, c.Cursor)).AtOffset(int64(idx))
	}
	if prev, ok := c.SegmentDigests[idx]; ok && prev != digest {
		return media.NewError(media.CodeCachePoison, media.PhaseCheckpoint,
			"segment digest changed between attempts").AtOffset(int64(idx))
	}
	c.SegmentDigests[idx] = digest
	c.Cursor = idx + 1
	c.LastCompleteIdx = idx
	c.CommittedBytes += bytes
	return nil
}

// SetStage records a stage status.
func (c *Checkpoint) SetStage(name string, state StageState) {
	if c.Stages == nil {
		c.Stages = map[string]StageState{}
	}
	c.Stages[name] = state
}

// Stage returns a stage's status, defaulting to pending.
func (c *Checkpoint) Stage(name string) StageState {
	if c.Stages == nil {
		return StagePending
	}
	if s, ok := c.Stages[name]; ok {
		return s
	}
	return StagePending
}

// ResumeRequest is what a new run expects to find in a checkpoint.
type ResumeRequest struct {
	ManifestDigest string
	InputDigest    string
	// MaxSegments is the segment count of the freshly parsed manifest; a
	// checkpoint that claims to be past the end is rejected.
	MaxSegments int
	// ManifestVersion is the freshly parsed document's format version. It is
	// only compared when the checkpoint recorded one, so older checkpoints
	// stay readable.
	ManifestVersion int
}

// ResumeDecision explains whether and where a run may resume.
type ResumeDecision struct {
	Resumed     bool   `json:"resumed"`
	FromSegment int    `json:"from_segment"`
	Reason      string `json:"reason"`
}

// ValidateResume compares a stored checkpoint against the fresh inputs. A
// changed manifest or a backwards cursor is refused outright; anything else
// (a missing stage, an empty checkpoint) starts over from a safe point.
func ValidateResume(cp *Checkpoint, req ResumeRequest) (ResumeDecision, error) {
	if cp == nil {
		return ResumeDecision{FromSegment: 0, Reason: "no checkpoint"}, nil
	}
	if cp.Version != CheckpointVersion {
		return ResumeDecision{}, media.NewError(media.CodeManifestChg, media.PhaseCheckpoint,
			fmt.Sprintf("checkpoint schema %d != %d", cp.Version, CheckpointVersion)).AtOffset(0)
	}
	if cp.ManifestVersion != 0 && req.ManifestVersion != 0 && cp.ManifestVersion != req.ManifestVersion {
		return ResumeDecision{}, media.NewError(media.CodeManifestChg, media.PhaseCheckpoint,
			"manifest format version changed since checkpoint").AtOffset(0)
	}
	if cp.ManifestDigest != req.ManifestDigest {
		return ResumeDecision{}, media.NewError(media.CodeManifestChg, media.PhaseCheckpoint,
			"manifest changed since checkpoint").AtOffset(0).Because(
			fmt.Errorf("checkpoint %s, current %s", cp.ManifestDigest, req.ManifestDigest))
	}
	if req.InputDigest != "" && cp.InputDigest != req.InputDigest {
		return ResumeDecision{}, media.NewError(media.CodeManifestChg, media.PhaseCheckpoint,
			"input digest changed since checkpoint").AtOffset(0)
	}
	if cp.Cursor < 0 || cp.Cursor > req.MaxSegments {
		return ResumeDecision{}, media.NewError(media.CodeCursorBack, media.PhaseCheckpoint,
			fmt.Sprintf("checkpoint cursor %d outside 0..%d", cp.Cursor, req.MaxSegments)).AtOffset(int64(cp.Cursor))
	}
	if cp.LastCompleteIdx+1 != cp.Cursor {
		return ResumeDecision{}, media.NewError(media.CodeCursorBack, media.PhaseCheckpoint,
			"checkpoint cursor is not adjacent to the last complete segment").AtOffset(int64(cp.Cursor))
	}
	if cp.Cursor == req.MaxSegments {
		return ResumeDecision{Resumed: true, FromSegment: cp.Cursor, Reason: "checkpoint already complete"}, nil
	}
	if cp.Cursor == 0 {
		return ResumeDecision{FromSegment: 0, Reason: "checkpoint has no committed segments"}, nil
	}
	// Every committed segment's digest must still be present and stable.
	idx := make([]int, 0, len(cp.SegmentDigests))
	for k := range cp.SegmentDigests {
		idx = append(idx, k)
	}
	sort.Ints(idx)
	for i, k := range idx {
		if i != k {
			return ResumeDecision{}, media.NewError(media.CodeCursorBack, media.PhaseCheckpoint,
				fmt.Sprintf("segment digests are not contiguous at %d", k)).AtOffset(int64(k))
		}
		if cp.SegmentDigests[k] == "" {
			return ResumeDecision{}, media.NewError(media.CodeCursorBack, media.PhaseCheckpoint,
				fmt.Sprintf("segment %d has no digest", k)).AtOffset(int64(k))
		}
	}
	return ResumeDecision{
		Resumed: true, FromSegment: cp.Cursor,
		Reason: fmt.Sprintf("resume from last complete segment %d", cp.LastCompleteIdx),
	}, nil
}

// Save atomically writes the checkpoint to path.
func (c *Checkpoint) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "encode checkpoint").AtOffset(0).Because(err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "create checkpoint directory").AtOffset(0).Because(err)
	}
	tmp, err := os.CreateTemp(dir, ".ckpt-*")
	if err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "create checkpoint temp").AtOffset(0).Because(err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "write checkpoint temp").AtOffset(0).Because(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "sync checkpoint temp").AtOffset(0).Because(err)
	}
	if err := tmp.Close(); err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "close checkpoint temp").AtOffset(0).Because(err)
	}
	if err := os.Rename(name, path); err != nil {
		return media.NewError(media.CodeInternal, media.PhaseCheckpoint, "commit checkpoint").AtOffset(0).Because(err)
	}
	return nil
}

// Load reads a checkpoint. A missing file is not an error: it means fresh run.
func Load(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, media.NewError(media.CodeInternal, media.PhaseCheckpoint, "read checkpoint").AtOffset(0).Because(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, media.NewError(media.CodeInternal, media.PhaseCheckpoint, "corrupt checkpoint file").AtOffset(0).Because(err)
	}
	return &cp, nil
}
