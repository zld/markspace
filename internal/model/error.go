// Package model contains the integer media-time primitives and the shared
// structured error type used by every processing stage.
package model

import (
	"errors"
	"fmt"
	"strings"
)

// Stage identifies the subsystem that produced an error.
type Stage string

const (
	StageModel   Stage = "model"
	StageParse   Stage = "parse"
	StageScan    Stage = "scan"
	StageAlign   Stage = "align"
	StageVerify  Stage = "verify"
	StageResume  Stage = "resume"
	StagePublish Stage = "publish"
)

// Code is a stable, machine-readable error classification.
type Code string

const (
	CodeZeroTimescale      Code = "zero_timescale"
	CodeTimeOverflow       Code = "time_overflow"
	CodeNegativeDuration   Code = "negative_duration"
	CodeMPDInvalid         Code = "mpd_invalid"
	CodeAlignInput         Code = "align_input"
	CodeMPDTruncated       Code = "mpd_truncated"
	CodeMissingIndex       Code = "missing_index"
	CodeHLSInvalid         Code = "hls_invalid"
	CodeHLSTruncated       Code = "hls_truncated"
	CodeInvalidEXTINF      Code = "invalid_extinf"
	CodeInvalidMediaSeq    Code = "invalid_media_sequence"
	CodeUnknownMediaSeq    Code = "unknown_media_sequence"
	CodeDuplicateMediaSeq  Code = "duplicate_media_sequence"
	CodeOrphanURI          Code = "orphan_uri"
	CodeTruncatedBox       Code = "truncated_box"
	CodeInvalidBoxSize     Code = "invalid_box_size"
	CodeIncompleteSamples  Code = "incomplete_sample_table"
	CodeBoundaryDrift      Code = "boundary_drift"
	CodeHLSMismatch        Code = "hls_mismatch"
	CodeTimeRegression     Code = "time_regression"
	CodeSequenceNotInc     Code = "sequence_not_increasing"
	CodeUntraceable        Code = "untraceable_entry"
	CodeInputChanged       Code = "input_changed"
	CodeManifestInvalid    Code = "manifest_invalid"
	CodePathEscape         Code = "path_escape"
	CodeResumeInconsistent Code = "resume_inconsistent"
)

// NoOffset marks a diagnostic that cannot be tied to a byte position.
const NoOffset int64 = -1

// Error is the single error type carried across stage boundaries. It keeps
// the input path, the byte offset into the original input, the processing
// stage and whether the pipeline may continue past the failure.
type Error struct {
	Code      Code   `json:"code"`
	Stage     Stage  `json:"stage"`
	Path      string `json:"path,omitempty"`
	Offset    int64  `json:"offset"`
	Message   string `json:"message"`
	Recovered bool   `json:"recoverable"`
	Err       error  `json:"-"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Path != "" {
		b.WriteString(" ")
		b.WriteString(e.Path)
	}
	if e.Offset >= 0 {
		fmt.Fprintf(&b, "@%d", e.Offset)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Unwrap exposes the underlying cause for errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// WithPath returns a copy carrying the input path.
func (e *Error) WithPath(path string) *Error {
	c := *e
	if c.Path == "" {
		c.Path = path
	}
	return &c
}

// WithOffset returns a copy carrying the byte offset when none is set yet.
func (e *Error) WithOffset(offset int64) *Error {
	c := *e
	if c.Offset == NoOffset {
		c.Offset = offset
	}
	return &c
}

// newError constructs a structured error.
func newError(code Code, path string, offset int64, stage Stage, recoverable bool, msg string) *Error {
	return &Error{Code: code, Path: path, Offset: offset, Stage: stage, Recovered: recoverable, Message: msg}
}

// NewError exposes structured error construction to sibling packages.
func NewError(code Code, path string, offset int64, stage Stage, recoverable bool, msg string) *Error {
	return newError(code, path, offset, stage, recoverable, msg)
}

// Wrap converts err into a structured error, preserving an existing *Error.
func Wrap(err error, code Code, path string, offset int64, stage Stage, recoverable bool) *Error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		c := *existing
		if c.Path == "" {
			c.Path = path
		}
		if c.Offset == NoOffset {
			c.Offset = offset
		}
		return &c
	}
	return &Error{Code: code, Path: path, Offset: offset, Stage: stage,
		Recovered: recoverable, Message: err.Error(), Err: err}
}

// IsRecoverable reports whether processing may continue after e.
func IsRecoverable(e *Error) bool { return e != nil && e.Recovered }

// SortErrors orders diagnostics deterministically: by offset (unknown last),
// then code, then path, then message.
func SortErrors(errs []*Error) {
	stableSort(errs, func(a, b *Error) bool {
		ao, bo := a.Offset, b.Offset
		if ao != bo {
			if ao == NoOffset {
				return false
			}
			if bo == NoOffset {
				return true
			}
			return ao < bo
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
}
