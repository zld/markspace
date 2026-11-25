// Package export performs dependency-free offscreen frame/audio export and
// computes deterministic statistics with input/stage provenance.
package export

import (
	"github.com/zld/media-check/internal/fault"
)

type Provenance struct {
	InputID     string      `json:"input_id"`
	InputSHA256 string      `json:"input_sha256"`
	Stage       fault.Stage `json:"stage"`
	Artifact    string      `json:"artifact,omitempty"`
	ByteOffset  int64       `json:"byte_offset,omitempty"`
	DerivedFrom []string    `json:"derived_from,omitempty"`
}

type FrameStat struct {
	Index      int        `json:"index"`
	TrackID    uint32     `json:"track_id"`
	PTSMS      int64      `json:"pts_ms"`
	SHA256     string     `json:"sha256"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	PixelMin   int        `json:"pixel_min"`
	PixelMax   int        `json:"pixel_max"`
	PixelMean  float64    `json:"pixel_mean"`
	Artifact   string     `json:"artifact"`
	Provenance Provenance `json:"provenance"`
}

type SegmentStat struct {
	SegmentID       string     `json:"segment_id"`
	MaterialID      string     `json:"material_id"`
	SHA256          string     `json:"segment_sha256"`
	MaterialSHA256  string     `json:"material_sha256"`
	FrameHashes     []string   `json:"frame_hashes"`
	TimelineStartMS int64      `json:"timeline_start_ms"`
	TimelineEndMS   int64      `json:"timeline_end_ms"`
	Provenance      Provenance `json:"provenance"`
}

type AudioStat struct {
	TrackID    uint32     `json:"track_id"`
	Samples    int        `json:"samples"`
	Peak       int        `json:"peak"`
	PeakDBFS   float64    `json:"peak_dbfs"`
	RMS        float64    `json:"rms"`
	SHA256     string     `json:"sha256"`
	Artifact   string     `json:"artifact,omitempty"`
	Provenance Provenance `json:"provenance"`
}

type PixelSummary struct {
	Min  int     `json:"min"`
	Max  int     `json:"max"`
	Mean float64 `json:"mean"`
}

type Result struct {
	Frames              []FrameStat   `json:"frames"`
	Segments            []SegmentStat `json:"segments"`
	Audio               AudioStat     `json:"audio"`
	Pixels              PixelSummary  `json:"pixels"`
	FrameSHA256         string        `json:"frame_sha256"`
	SegmentSHA256       string        `json:"segment_sha256"`
	AlignmentErrorMS    int64         `json:"alignment_error_ms"`
	AlignmentProvenance Provenance    `json:"alignment_provenance"`
}
