// Package fault defines cross-module error categories and structured locations.
package fault

import (
	"errors"
	"fmt"
)

// Kind is a stable, machine-readable error classification.
type Kind string

const (
	Unknown             Kind = "unknown"
	InvalidTimecode     Kind = "invalid_timecode"
	InvalidDropMarker   Kind = "invalid_drop_marker"
	NegativeTime        Kind = "negative_time"
	OutOfRange          Kind = "out_of_range"
	SubtitleParse       Kind = "subtitle_parse"
	ZeroDurationCue     Kind = "zero_duration_cue"
	OverlappingCue      Kind = "overlapping_cue"
	DigestMismatch      Kind = "digest_mismatch"
	ArtifactMissing     Kind = "artifact_missing"
	QueueCanceled       Kind = "queue_canceled"
	CorruptFrame        Kind = "corrupt_frame"
	LayerFailed         Kind = "layer_failed"
	MissingSegment      Kind = "missing_segment"
	DuplicateSequence   Kind = "duplicate_rtp_sequence"
	TimestampRegression Kind = "rtp_timestamp_regression"
	BoundaryMismatch    Kind = "boundary_mismatch"
	InvalidMP4          Kind = "invalid_mp4"
	Checkpoint          Kind = "checkpoint"
	Publish             Kind = "publish"
)

// Error carries enough location information for terminal and JSON reports.
//
// Offset is a zero-based byte offset for source files. For streaming and
// boundary errors it may instead hold a segment-relative tick offset; the
// field name in the surrounding report disambiguates the unit.
type Error struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
	Layer   string `json:"layer,omitempty"`
	Segment *int   `json:"segment,omitempty"`
	Line    int    `json:"line,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	Err     error  `json:"-"`
}

func (e *Error) Error() string {
	s := string(e.Kind) + ": " + e.Message
	if e.Source != "" {
		s += fmt.Sprintf(" (source=%s offset=%d", e.Source, e.Offset)
		if e.Line > 0 {
			s += fmt.Sprintf(" line=%d", e.Line)
		}
		s += ")"
	}
	if e.Layer != "" {
		s += fmt.Sprintf(" (layer=%s", e.Layer)
		if e.Segment != nil {
			s += fmt.Sprintf(" segment=%d", *e.Segment)
		}
		if e.Offset != 0 {
			s += fmt.Sprintf(" offset=%d", e.Offset)
		}
		s += ")"
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// New creates a classified error.
func New(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Wrap classifies an existing error.
func Wrap(kind Kind, err error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Err: err}
}

// At creates a source-located classified error.
func At(kind Kind, source string, offset int64, format string, args ...any) *Error {
	return &Error{Kind: kind, Source: source, Offset: offset, Message: fmt.Sprintf(format, args...)}
}

// InLayer creates a layer/segment-located error. Segment numbers are
// one-based in user-facing reports.
func InLayer(kind Kind, layer string, segment int, offset int64, format string, args ...any) *Error {
	return &Error{
		Kind:    kind,
		Layer:   layer,
		Segment: &segment,
		Offset:  offset,
		Message: fmt.Sprintf(format, args...),
	}
}

// Classify returns the stable Kind for err, or Unknown.
func Classify(err error) Kind {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Kind
	}
	return Unknown
}

// ExitCode maps verification failures to the acceptance-required status.
func ExitCode(err error) int {
	switch Classify(err) {
	case BoundaryMismatch, MissingSegment, DuplicateSequence, TimestampRegression,
		InvalidMP4, DigestMismatch:
		return 2
	default:
		return 1
	}
}
