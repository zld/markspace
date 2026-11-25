// Package manifest performs deterministic merging and local archival of
// fingerprints, diagnostics, exports, and checkpoint provenance.
package manifest

import (
	"github.com/zld/media-check/internal/diagnostic"
	"github.com/zld/media-check/internal/export"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/fingerprint"
	"github.com/zld/media-check/internal/timeline"
)

const SchemaVersion = 1

type InputRecord struct {
	ID          string           `json:"id"`
	Path        string           `json:"path"`
	Size        int64            `json:"size"`
	Fingerprint fingerprint.Hash `json:"fingerprint"`
	Role        string           `json:"role"`
}

type TimelineRef struct {
	SegmentID string `json:"segment_id"`
	TrackID   uint32 `json:"track_id"`
	StartMS   int64  `json:"start_ms"`
	EndMS     int64  `json:"end_ms"`
}

type FingerprintGroup struct {
	Algorithm     string        `json:"algorithm"`
	ContentSHA256 string        `json:"content_sha256"`
	MaterialID    string        `json:"material_id,omitempty"`
	Count         int           `json:"count"`
	TimelineRefs  []TimelineRef `json:"timeline_refs"`
}

type CheckpointInfo struct {
	LastStage       string   `json:"last_stage"`
	CompletedStages []string `json:"completed_stages"`
	// Resumed is process/provenance information but intentionally excluded
	// from the stable manifest bytes so resumed and clean runs match.
	Resumed bool `json:"-"`
}

type Summary struct {
	Tracks           int     `json:"tracks"`
	Frames           int     `json:"frames"`
	DuplicateGroups  int     `json:"duplicate_groups"`
	AlignmentErrorMS int64   `json:"alignment_error_ms"`
	PixelMin         int     `json:"pixel_min"`
	PixelMax         int     `json:"pixel_max"`
	PixelMean        float64 `json:"pixel_mean"`
	PeakDBFS         float64 `json:"peak_dbfs"`
	FrameSHA256      string  `json:"frame_sha256"`
	SegmentSHA256    string  `json:"segment_sha256"`
}

// Manifest is the stable archived document. It contains no wall-clock time,
// random map iteration, absolute work directory, or process identity.
type Manifest struct {
	SchemaVersion     int                       `json:"schema_version"`
	TaskKey           string                    `json:"task_key"`
	Scope             string                    `json:"scope"`
	Corruption        string                    `json:"corruption,omitempty"`
	Inputs            []InputRecord             `json:"inputs"`
	Summary           Summary                   `json:"summary"`
	Tracks            []timeline.TrackView      `json:"tracks"`
	DuplicateGroups   []timeline.DuplicateGroup `json:"duplicate_groups"`
	FingerprintGroups []FingerprintGroup        `json:"fingerprint_groups"`
	Export            *export.Result            `json:"export,omitempty"`
	Diagnostics       *diagnostic.Report        `json:"diagnostics,omitempty"`
	Issues            []*fault.Error            `json:"issues,omitempty"`
	Checkpoint        CheckpointInfo            `json:"checkpoint"`
	Stages            []string                  `json:"stages"`
}
