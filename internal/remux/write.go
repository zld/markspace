// Package remux writes lossless MP4/MOV output from indexed samples.
package remux

import (
	"errors"

	"mediacore/internal/fault"
	"mediacore/internal/scan"
)

// ErrInterrupt marks a simulated stop after a complete sample boundary.
var ErrInterrupt = errors.New("simulated interrupt after complete sample")

// Options controls resumable remux output.
type Options struct {
	OutputPath     string
	AllowedRoot    string
	Resume         bool
	AfterSample    func(committed int) error
	CheckpointPath string
}

// Result describes a completed or interrupted remux.
type Result struct {
	OutputPath     string `json:"output_path"`
	Bytes          int64  `json:"bytes"`
	SampleCount    int    `json:"sample_count"`
	Resumed        bool   `json:"resumed"`
	OutputSHA256   string `json:"output_sha256,omitempty"`
	CheckpointPath string `json:"checkpoint_path,omitempty"`
}

type plannedSample struct {
	ref     scan.SampleRef
	track   scan.Track
	mapping scan.TrackMapping
	offset  uint32
	data    []byte
}

// WriteMP4 is the compilable skeleton; orchestration lands in the next batch.
func WriteMP4(_ *scan.Index, _ []scan.TrackMapping, _ []scan.SampleRef, _ Options) (*Result, error) {
	return nil, fault.New(fault.UnsupportedFormat, "remux writer skeleton")
}
