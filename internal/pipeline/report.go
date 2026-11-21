// Package pipeline wires configuration, TS parsing, IDR slicing, playlist
// generation, integrity indexing and cross-rendition verification into the
// build and verify commands.
package pipeline

import (
	"time"

	"hlsflow/internal/config"
)

// Failure is one verification or ingest problem, keyed to rendition and the
// source segment sequence where it was observed.
type Failure struct {
	Rendition string `json:"rendition"`
	Segment   int    `json:"segment"`
	Reason    string `json:"reason"`
}

// RenditionReport is the terminal/JSON line for one ladder rung.
type RenditionReport struct {
	Rendition        string    `json:"rendition"`
	Bitrate          int       `json:"bitrate"`
	Segments         int       `json:"segments"`
	DurationSeconds  float64   `json:"duration"`
	DriftSeconds     float64   `json:"drift"`
	Missing          int       `json:"missing"`
	Reordered        int       `json:"reordered"`
	ChecksumFailures int       `json:"checksum_failures"`
	Failures         []Failure `json:"failures,omitempty"`
}

// Report is the aggregate result of a build or verify run.
type Report struct {
	Renditions []RenditionReport `json:"renditions"`
	// ExtraFiles are output files that no playlist references.
	ExtraFiles []string `json:"extra_files,omitempty"`
	// job is retained so verification can apply configured in-memory faults.
	job *config.Job
}

// Options controls a build run.
type Options struct {
	JobPath        string
	InputDir       string
	OutputDir      string
	TargetDuration time.Duration
	GapTimeout     time.Duration
	Finalize       bool
}

// HasFailures reports whether any rendition recorded a problem.
func (r *Report) HasFailures() bool {
	for _, rr := range r.Renditions {
		if len(rr.Failures) > 0 || rr.ChecksumFailures > 0 {
			return true
		}
	}
	return false
}

// addFailure appends a failure and updates the owning report line.
func (r *Report) addFailure(rendition string, f Failure) {
	for i := range r.Renditions {
		if r.Renditions[i].Rendition == rendition {
			r.Renditions[i].Failures = append(r.Renditions[i].Failures, f)
			return
		}
	}
}

// ensureRendition lazily creates a report line.
func (r *Report) ensureRendition(rd config.Rendition) *RenditionReport {
	for i := range r.Renditions {
		if r.Renditions[i].Rendition == rd.Name {
			return &r.Renditions[i]
		}
	}
	r.Renditions = append(r.Renditions, RenditionReport{Rendition: rd.Name, Bitrate: rd.Bitrate})
	return &r.Renditions[len(r.Renditions)-1]
}
