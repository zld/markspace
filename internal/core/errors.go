// Package core defines the shared error model and the continuity kernel.
//
// Error model: every failure carries a Stage (pipeline phase), a Code
// (machine-checkable), the Source record, an optional manifest Line and
// byte Offset so an anomaly can be traced to one record, line or byte.
package core

import "fmt"

// Stage identifies the pipeline phase that raised an error.
type Stage string

const (
	StageManifest Stage = "manifest"
	StageParse    Stage = "parse"
	StageTimebase Stage = "timebase"
	StageCore     Stage = "core"
)

// Code is a machine-checkable error identifier.
type Code string

const (
	CodePathEscape    Code = "path_escape"
	CodeBadManifest   Code = "bad_manifest"
	CodeOversize      Code = "segment_too_large"
	CodeTruncatedBox  Code = "truncated_box"
	CodeAmbiguousJump Code = "ambiguous_jump"
	CodeInput         Code = "input_error"
)

// Error is the shared error model exchanged between adapters and the kernel.
// Line is 1-based (0 = n/a); Offset is a byte offset (-1 = n/a).
type Error struct {
	Stage  Stage
	Code   Code
	Source string
	Line   int
	Offset int64
	Msg    string
	Err    error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("stage=%s code=%s source=%s", e.Stage, e.Code, e.Source)
	if e.Line > 0 {
		s += fmt.Sprintf(" line=%d", e.Line)
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" byte=%d", e.Offset)
	}
	if e.Msg != "" {
		s += " " + e.Msg
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds an Error; pass line=0 and offset=-1 when not applicable.
func NewError(stage Stage, code Code, source string, line int, offset int64, msg string, err error) *Error {
	return &Error{Stage: stage, Code: code, Source: source, Line: line, Offset: offset, Msg: msg, Err: err}
}
