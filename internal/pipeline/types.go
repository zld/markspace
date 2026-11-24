// Package pipeline orchestrates the processing stages (manifest
// timeline, fragment parse+repair, index rebuild, verification) with
// journaled resume and atomic output commit.
package pipeline

import (
	"mediarun/internal/clock"
	"mediarun/internal/repair"
	"mediarun/internal/verify"
)

// Stage names and versions. Bumping a version invalidates cached stages.
const (
	StageManifest  = "manifest"
	StageFragments = "fragments"
	StageIndex     = "index"
	StageVerify    = "verify"

	VersionManifest  = 1
	VersionFragments = 1
	VersionIndex     = 1
	VersionVerify    = 1
)

// FileDigest pins one input file into the provenance trail.
type FileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// SegTimeline is one DASH segment mapped onto the unified timeline.
type SegTimeline struct {
	Media         string      `json:"media"`
	Number        int64       `json:"number"`
	DeclaredStart clock.Ticks `json:"declaredStart"`
	Duration      clock.Ticks `json:"duration"`
	MPDOffset     int64       `json:"mpdOffset"`
}

// RepTimeline is one DASH representation on the unified timeline.
type RepTimeline struct {
	ID        string        `json:"id"`
	Bandwidth int64         `json:"bandwidth"`
	Timescale int64         `json:"timescale"`
	Init      string        `json:"init"`
	Segments  []SegTimeline `json:"segments"`
}

// HLSSegTimeline is one HLS segment on the unified play timeline.
type HLSSegTimeline struct {
	URI           string      `json:"uri"`
	DeclaredStart clock.Ticks `json:"declaredStart"`
	Duration      clock.Ticks `json:"duration"`
	DiscSeq       int         `json:"discSeq"`
	Offset        int64       `json:"offset"`
}

// HLSTimeline is one playlist's timeline.
type HLSTimeline struct {
	Playlist string           `json:"playlist"`
	Segments []HLSSegTimeline `json:"segments"`
}

// Timeline is the output of the manifest stage.
type Timeline struct {
	Manifests []FileDigest  `json:"manifests"`
	Reps      []RepTimeline `json:"reps"`
	HLS       []HLSTimeline `json:"hls"`
}

// SyncPoint is a random-access point ready for the index stage.
type SyncPoint struct {
	Tick        clock.Ticks `json:"tick"`
	MoofOffset  int64       `json:"moofOffset"`
	SampleIndex int         `json:"sampleIndex"`
	DataOffset  int64       `json:"dataOffset"`
}

// SegmentResult is the per-segment parse/repair outcome.
type SegmentResult struct {
	Path             string      `json:"path"`
	Digest           string      `json:"digest"`
	DeclaredStart    clock.Ticks `json:"declaredStart"`
	ActualStart      clock.Ticks `json:"actualStart"`
	Duration         clock.Ticks `json:"duration"` // measured media duration
	AlignmentErrorMs int64       `json:"alignmentErrorMs"`
	DiscontinuitySeq int         `json:"discontinuitySeq,omitempty"`
	DiscontinuityOff clock.Ticks `json:"discontinuityOffset,omitempty"`
	SyncPoints       []SyncPoint `json:"syncPoints,omitempty"`
}

// TrackResult is the per-track outcome of the fragments stage.
type TrackResult struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"` // dash | hls
	Bandwidth   int64           `json:"bandwidth,omitempty"`
	Timescale   int64           `json:"timescale"`
	MediaBytes  int64           `json:"mediaBytes"`
	SampleCount int             `json:"sampleCount"`
	Duration    clock.Ticks     `json:"durationTicks"`
	RepairCount int             `json:"repairCount"`
	Repairs     []repair.Entry  `json:"repairs,omitempty"`
	Stopped     bool            `json:"stopped"`
	Segments    []SegmentResult `json:"segments"`
}

// FragmentsResult is the output of the fragments stage.
type FragmentsResult struct {
	Tracks []TrackResult `json:"tracks"`
}

// Report is the committed final report with the full provenance trail.
type Report struct {
	InputDigest     string                    `json:"inputDigest"`
	ParamDigest     string                    `json:"paramDigest"`
	Manifests       []FileDigest              `json:"manifests"`
	Fragments       []FileDigest              `json:"fragments"`
	RepairMap       map[string][]repair.Entry `json:"repairMap,omitempty"`
	RepairMapDigest string                    `json:"repairMapDigest"`
	IndexDigest     string                    `json:"indexDigest"`
	Metrics         verify.Metrics            `json:"metrics"`
	// ReusedStages is run-local and deliberately excluded from the
	// committed report so the output digest stays identical on reruns.
	ReusedStages int `json:"-"`
}
