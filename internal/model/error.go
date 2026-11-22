// Package model defines the shared error model, limits and value types used by
// every archivist subsystem. It must not import any other internal package.
package model

import (
	"errors"
	"fmt"
)

// Processing stages. They appear verbatim in ArchiveError.Stage and reports.
const (
	StageFixture   = "fixture"
	StageContainer = "container"
	StageIndex     = "sample-index"
	StageTimeline  = "timeline"
	StageSegment   = "segmenter"
	StageManifest  = "manifest"
	StageDAG       = "dag"
)

// Sentinel error kinds. ArchiveError wraps one of these so errors.Is keeps
// working across subsystem boundaries.
var (
	ErrZeroTimescale       = errors.New("zero timescale")
	ErrTimestampOverflow   = errors.New("timestamp conversion overflow")
	ErrNonMonotonic        = errors.New("non-monotonic timestamps")
	ErrTruncatedBox        = errors.New("truncated box")
	ErrCorruptSampleTable  = errors.New("corrupt sample table")
	ErrMissingKeyframeIdx  = errors.New("missing keyframe index")
	ErrNoCommonKeyframe    = errors.New("no common keyframe boundary")
	ErrLimitExceeded       = errors.New("archive limit exceeded")
	ErrOutOfBoundsOffset   = errors.New("sample offset out of bounds")
	ErrPermanentFailure    = errors.New("permanent node failure")
	ErrTransientFailure    = errors.New("transient node failure")
	ErrManifestBlocked     = errors.New("manifest publication blocked")
	ErrUnsupportedBox      = errors.New("unsupported box layout")
)

// TimeRange is expressed in the owning track's timescale units.
type TimeRange struct {
	Start      uint64
	End        uint64
	Timescale  uint32
}

// ArchiveError carries the stage, track and sample position where a failure
// occurred, plus an optional media-time range for diagnostics.
type ArchiveError struct {
	Stage     string
	Kind      error
	TrackID   uint32
	SampleIdx uint32
	Range     *TimeRange
	Detail    string
}

func (e *ArchiveError) Error() string {
	s := e.Stage + ": " + e.Kind.Error()
	if e.TrackID != 0 {
		s += fmt.Sprintf(" (track=%d", e.TrackID)
		if e.SampleIdx != 0 {
			s += fmt.Sprintf(" sample=%d", e.SampleIdx)
		}
		if e.Range != nil {
			s += fmt.Sprintf(" dts=[%d,%d]@%d", e.Range.Start, e.Range.End, e.Range.Timescale)
		}
		s += ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Unwrap exposes the wrapped sentinel so errors.Is/As work.
func (e *ArchiveError) Unwrap() error { return e.Kind }

// AtTrack annotates an error with a track id and returns a new ArchiveError.
func AtTrack(stage string, kind error, trackID uint32, detail string) *ArchiveError {
	return &ArchiveError{Stage: stage, Kind: kind, TrackID: trackID, Detail: detail}
}

// AtSample annotates an error with track, sample position and time range.
func AtSample(stage string, kind error, trackID uint32, sampleIdx uint32, tr TimeRange) *ArchiveError {
	return &ArchiveError{Stage: stage, Kind: kind, TrackID: trackID,
		SampleIdx: sampleIdx, Range: &tr}
}
