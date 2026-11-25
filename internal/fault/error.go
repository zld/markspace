// Package fault defines the structured error/diagnostic model used across
// ingestion, parsing, scanning, diagnosis, and archival.
package fault

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zld/media-check/internal/clock"
)

type Stage string

const (
	StageIngest      Stage = "ingest"
	StageMP4         Stage = "mp4"
	StageScan        Stage = "scan"
	StageTimeline    Stage = "timeline"
	StageDiagnostic  Stage = "diagnostic"
	StageFingerprint Stage = "fingerprint"
	StageJobs        Stage = "jobs"
	StageManifest    Stage = "manifest"
	StageExport      Stage = "export"
)

type Code string

const (
	CodeInvalidArgument     Code = "invalid_argument"
	CodeMemoryLimit         Code = "memory_limit"
	CodeReadFailed          Code = "read_failed"
	CodeInvalidBox          Code = "invalid_box"
	CodeTruncatedBox        Code = "truncated_box"
	CodeOffsetOutOfRange    Code = "offset_out_of_range"
	CodeInvalidTimescale    Code = "invalid_timescale"
	CodeSampleOutOfRange    Code = "sample_out_of_range"
	CodeEditOutOfRange      Code = "edit_out_of_range"
	CodeInvalidOverlap      Code = "invalid_overlap"
	CodeUnreadableSample    Code = "unreadable_sample"
	CodeMissingKeyframe     Code = "missing_keyframe"
	CodeKeyframeInterval    Code = "keyframe_interval_exceeded"
	CodeTimestampRegression Code = "timestamp_regression"
	CodeCheckpointMismatch  Code = "checkpoint_fingerprint_mismatch"
	CodeCheckpointNotFound  Code = "checkpoint_not_found"
	CodeManifestMismatch    Code = "manifest_mismatch"
)

// Error is the only error shape expected to cross package and process
// boundaries for malformed media or state.
type Error struct {
	Code      Code        `json:"code"`
	Stage     Stage       `json:"stage"`
	Message   string      `json:"message"`
	Path      string      `json:"path,omitempty"`
	InputID   string      `json:"input_id,omitempty"`
	InputSHA  string      `json:"input_sha256,omitempty"`
	TrackID   uint32      `json:"track_id,omitempty"`
	Offset    int64       `json:"byte_offset,omitempty"`
	Length    int64       `json:"byte_length,omitempty"`
	BoxOffset int64       `json:"box_offset,omitempty"`
	TimeRange clock.Range `json:"time_range,omitempty"`

	// Prev* describes the previous sample when detecting regression/gaps.
	PrevOffset int64       `json:"previous_byte_offset,omitempty"`
	PrevRange  clock.Range `json:"previous_time_range,omitempty"`

	Cause string `json:"cause,omitempty"`
	err   error
}

func New(stage Stage, code Code, format string, args ...any) *Error {
	return &Error{Stage: stage, Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil fault>"
	}
	msg := fmt.Sprintf("%s/%s: %s", e.Stage, e.Code, e.Message)
	if e.TrackID != 0 {
		msg += fmt.Sprintf(" track_id=%d", e.TrackID)
	}
	if e.Offset > 0 {
		msg += fmt.Sprintf(" byte_offset=%d", e.Offset)
	}
	if e.err != nil {
		msg += ": " + e.err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.err }

type Option func(*Error)

func WithPath(path string) Option {
	return func(e *Error) { e.Path = path }
}

func WithInput(id, sha string) Option {
	return func(e *Error) { e.InputID, e.InputSHA = id, sha }
}

func WithTrack(trackID uint32) Option {
	return func(e *Error) { e.TrackID = trackID }
}

func WithOffset(offset, length int64) Option {
	return func(e *Error) { e.Offset, e.Length = offset, length }
}

func WithBoxOffset(offset int64) Option {
	return func(e *Error) { e.BoxOffset = offset }
}

func WithTimeRange(r clock.Range) Option {
	return func(e *Error) { e.TimeRange = r }
}

func WithPrevious(offset int64, r clock.Range) Option {
	return func(e *Error) { e.PrevOffset, e.PrevRange = offset, r }
}

func WithCause(err error) Option {
	return func(e *Error) {
		e.err = err
		if err != nil {
			e.Cause = err.Error()
		}
	}
}

func E(stage Stage, code Code, message string, opts ...Option) *Error {
	e := &Error{Stage: stage, Code: code, Message: message}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Wrap preserves an existing *Error and adds options. Non-fault errors are
// converted to a structured read/internal error.
func Wrap(stage Stage, code Code, err error, message string, opts ...Option) *Error {
	var fe *Error
	if errors.As(err, &fe) {
		for _, opt := range opts {
			opt(fe)
		}
		if message != "" {
			fe.Message = message + ": " + fe.Message
		}
		return fe
	}
	e := E(stage, code, message, append(opts, WithCause(err))...)
	return e
}

func As(err error) (*Error, bool) {
	var fe *Error
	return fe, errors.As(err, &fe)
}

func IsCode(err error, code Code) bool {
	fe, ok := As(err)
	return ok && fe.Code == code
}

// JSON returns stable indented JSON for terminal/debug output.
func JSON(e *Error) string {
	if e == nil {
		return "{}"
	}
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"code":"%s","message":"%s"}`, e.Code, e.Message)
	}
	return string(b)
}
