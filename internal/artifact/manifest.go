// Package artifact defines the on-disk manifest shared by the renderer and the
// independent verifier. The renderer writes it; the verifier treats it as an
// untrusted claim and re-derives every value from the files.
package artifact

import (
	"medialab/internal/model"
	"medialab/internal/pixel"
)

// Version is bumped whenever the manifest schema changes incompatibly.
const Version = 1

// Params records every choice that influenced the artefacts, so a rerun with
// the same inputs and params can be shown to reproduce identical hashes.
type Params struct {
	Quality       float64 `json:"quality"`
	QuantStep     int     `json:"quantStep"`
	FPS           int     `json:"fps"`
	FillPolicy    string  `json:"fillPolicy"`
	Workers       int     `json:"workers"`
	MaxCacheBytes int64   `json:"maxCacheBytes"`
	ReorderWindow int     `json:"reorderWindow"`
	MaxDimension  int     `json:"maxDimension"`
	MaxBudgetMS   int64   `json:"maxBudgetMs"`
	MaxBytes      int64   `json:"maxBytes"`
}

// Frame is one exported frame with both source and artefact provenance.
type Frame struct {
	Track        model.TrackID `json:"track"`
	Sequence     uint32        `json:"sequence"`
	PTS          int64         `json:"pts"`
	Fill         bool          `json:"fill"`
	SourceSHA256 string        `json:"sourceSha256"`
	SHA256       string        `json:"sha256"`
	Path         string        `json:"path"`
	Bytes        int64         `json:"bytes"`
	Width        int           `json:"width"`
	Height       int           `json:"height"`
	Stats        pixel.Stats   `json:"stats"`
	LoudnessDBFS float64       `json:"loudnessDbfs"`
}

// Segment is one per-track MP4 clip.
type Segment struct {
	Track      model.TrackID `json:"track"`
	Path       string        `json:"path"`
	SHA256     string        `json:"sha256"`
	Bytes      int64         `json:"bytes"`
	Samples    int           `json:"samples"`
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	TimeScale  uint32        `json:"timescale"`
	DurationNS int64         `json:"durationNs"`
	FirstPTS   int64         `json:"firstPts"`
	LastPTS    int64         `json:"lastPts"`
}

// Summary is the aggregate a terminal reader checks at a glance.
type Summary struct {
	Tracks           int     `json:"tracks"`
	Frames           int     `json:"frames"`
	FillFrames       int     `json:"fillFrames"`
	MeanR            float64 `json:"meanR"`
	MeanG            float64 `json:"meanG"`
	MeanB            float64 `json:"meanB"`
	Variance         float64 `json:"variance"`
	PeakLoudnessDBFS float64 `json:"peakLoudnessDbfs"`
	MaxAlignErrorNS  int64   `json:"maxAlignErrorNs"`
	TotalBytes       int64   `json:"totalBytes"`
	PeakCacheBytes   int64   `json:"peakCacheBytes"`
	RunHash          string  `json:"runHash"`
}

// Manifest is written to <out>/manifest.json.
type Manifest struct {
	Version  int          `json:"version"`
	Params   Params       `json:"params"`
	Frames   []Frame      `json:"frames"`
	Segments []Segment    `json:"segments"`
	Summary  Summary      `json:"summary"`
	Warnings model.Faults `json:"warnings,omitempty"`
	Faults   model.Faults `json:"faults,omitempty"`
}
