package release

import (
	"assetpack/internal/clock"
)

const (
	ManifestName = "assetpack.json"
	StateName    = "state.json"
	CurrentLink  = "current"
	VersionsDir  = "versions"
	SchemaV1     = 1
)

// Artifact is a hashed file inside a version directory.
type Artifact struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Platform string `json:"platform,omitempty"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// Input records one source fixture hash.
type Input struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

// RGB is the channel mean on a 0..255 scale.
type RGB struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
}

// Segment records a platform segmentation boundary and member hash.
type Segment struct {
	Platform   string      `json:"platform"`
	Name       string      `json:"name"`
	Start      clock.Ticks `json:"start"`
	End        clock.Ticks `json:"end"`
	FrameCount int         `json:"frame_count"`
	Size       int64       `json:"size"`
	SHA256     string      `json:"sha256"`
	Members    []string    `json:"members,omitempty"`
}

// Target contains platform-specific counts and recomputable metrics.
type Target struct {
	Platform              string      `json:"platform"`
	FrameCount            int         `json:"frame_count"`
	SegmentCount          int         `json:"segment_count"`
	AudioSampleFrames     int         `json:"audio_sample_frames,omitempty"`
	Width                 int         `json:"width,omitempty"`
	Height                int         `json:"height,omitempty"`
	RGBMean               RGB         `json:"rgb_mean"`
	PeakDBFS              float64     `json:"peak_dbfs,omitempty"`
	ActualRange           clock.Range `json:"actual_range"`
	AlignmentErrorSamples int         `json:"alignment_error_samples"`
	AlignmentTolerance    int         `json:"alignment_tolerance"`
	QueuePeak             int         `json:"queue_peak,omitempty"`
	QueueCapacity         int         `json:"queue_capacity,omitempty"`
	Segments              []Segment   `json:"segments,omitempty"`
}

// Manifest is the hash-verified release inventory.
type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	Version       string      `json:"version"`
	Sequence      int         `json:"sequence"`
	TimebaseHz    int64       `json:"timebase_hz"`
	Range         clock.Range `json:"range"`
	Inputs        []Input     `json:"inputs"`
	Clips         []Artifact  `json:"clips,omitempty"`
	Artifacts     []Artifact  `json:"artifacts"`
	Targets       []Target    `json:"targets"`
}

// State is the atomically replaced current/previous version pointer.
type State struct {
	Current  string   `json:"current"`
	Previous string   `json:"previous,omitempty"`
	History  []string `json:"history,omitempty"`
}

// CommitRequest contains a complete in-memory package tree and manifest.
type CommitRequest struct {
	Root     string
	Manifest *Manifest
	Files    map[string][]byte
}

// CommitResult reports the committed version and root paths.
type CommitResult struct {
	VersionDir string
	Manifest   *Manifest
	State      State
	Reused     bool
}
