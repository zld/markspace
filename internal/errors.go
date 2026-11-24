// Package medpipe contains the media pipeline subsystems: scanning,
// normalization (clock/audio/color), index rebuilding and streaming execution.
package medpipe

import (
	"errors"
	"fmt"
)

// ErrorKind classifies a pipeline failure so callers can decide whether a
// re-run, a resume or an operator intervention is the appropriate recovery.
type ErrorKind string

const (
	// KindRecoverable describes a localized, repairable condition (for
	// example a truncated trailing unit). The pipeline drops the affected
	// unit and keeps processing the rest of the track.
	KindRecoverable ErrorKind = "recoverable"
	// KindFatal describes a condition that cannot be repaired from the
	// input alone (ambiguous timestamps, failed audio or path validation).
	KindFatal ErrorKind = "fatal"
)

// PipelineError is the single error model shared by every subsystem. It
// always carries the affected track and, where a byte position is known, the
// absolute offset inside the track file, plus an actionable suggestion.
type PipelineError struct {
	Kind       ErrorKind `json:"kind"`
	Track      string    `json:"track"`
	Offset     int64     `json:"offset"`
	OffsetSet  bool      `json:"offset_set"`
	Code       string    `json:"code"`
	Message    string    `json:"message"`
	Suggestion string    `json:"suggestion"`
}

// Error implements the error interface.
func (e *PipelineError) Error() string {
	loc := e.Track
	if e.OffsetSet {
		loc = fmt.Sprintf("%s@byte %d", e.Track, e.Offset)
	}
	return fmt.Sprintf("%s [%s] %s: %s — %s", e.Kind, e.Code, loc, e.Message, e.Suggestion)
}

// Is supports errors.Is against kind and code markers without unwrapping.
func (e *PipelineError) Is(target error) bool {
	var pe *PipelineError
	if errors.As(target, &pe) {
		if pe.Code != "" && pe.Code == e.Code {
			return true
		}
		if pe.Kind != "" && pe.Kind == e.Kind {
			return true
		}
	}
	return false
}

// Recoverable reports whether the pipeline may continue past this error.
func (e *PipelineError) Recoverable() bool { return e.Kind == KindRecoverable }

// ErrorBuilder accumulates per-track errors while keeping the construction
// call sites compact.
type ErrorBuilder struct {
	track string
}

// NewErrorBuilder returns a builder pinned to one track.
func NewErrorBuilder(track string) *ErrorBuilder { return &ErrorBuilder{track: track} }

func (b *ErrorBuilder) make(kind ErrorKind, code, msg, suggestion string, offset int64, hasOffset bool) *PipelineError {
	return &PipelineError{
		Kind:       kind,
		Track:      b.track,
		Offset:     offset,
		OffsetSet:  hasOffset,
		Code:       code,
		Message:    msg,
		Suggestion: suggestion,
	}
}

// Recoverable builds a localized recoverable error without a byte offset.
func (b *ErrorBuilder) Recoverable(code, msg, suggestion string) *PipelineError {
	return b.make(KindRecoverable, code, msg, suggestion, 0, false)
}

// RecoverableAt builds a localized recoverable error anchored at an offset.
func (b *ErrorBuilder) RecoverableAt(code, msg, suggestion string, offset int64) *PipelineError {
	return b.make(KindRecoverable, code, msg, suggestion, offset, true)
}

// Fatal builds a non-recoverable error without a byte offset.
func (b *ErrorBuilder) Fatal(code, msg, suggestion string) *PipelineError {
	return b.make(KindFatal, code, msg, suggestion, 0, false)
}

// FatalAt builds a non-recoverable error anchored at an offset.
func (b *ErrorBuilder) FatalAt(code, msg, suggestion string, offset int64) *PipelineError {
	return b.make(KindFatal, code, msg, suggestion, offset, true)
}

// Error code constants keep error identity stable across releases.
const (
	CodeTruncatedUnit   = "TRUNCATED_UNIT"
	CodeInvalidRTP      = "INVALID_RTP_PACKET"
	CodeUnknownCodec    = "UNKNOWN_CODEC"
	CodeTimestampRepair = "UNREPAIRABLE_TIMESTAMP"
	CodeTimestampWarn   = "TIMESTAMP_REPAIRED"
	CodeAudioRate       = "AUDIO_SAMPLE_RATE_MISMATCH"
	CodeAudioChannels   = "AUDIO_CHANNEL_MAP_INVALID"
	CodeColorConflict   = "COLOR_METADATA_CONFLICT"
	CodeColorUnknown    = "COLOR_METADATA_UNKNOWN"
	CodeEmptyStream     = "EMPTY_STREAM"
	CodePathDenied      = "PATH_OUTSIDE_SANDBOX"
	CodeDigestMismatch  = "INDEX_DIGEST_MISMATCH"
)

// HasFatal reports whether any error in the slice is non-recoverable.
func HasFatal(errs []*PipelineError) bool {
	for _, e := range errs {
		if e != nil && e.Kind == KindFatal {
			return true
		}
	}
	return false
}
