// Package merr defines the unified error taxonomy shared by every stage
// (timeline, demux, render, manifest, pipeline). Each error carries the
// stage and, when known, the track it refers to, and maps to a stable
// process exit code: 2 param, 3 corrupt asset/graph, 4 resume failure.
package merr

import (
	"errors"
	"fmt"
)

// Kind classifies errors across stages.
type Kind int

const (
	KindInternal Kind = iota
	KindParam
	KindCorrupt
	KindResume
)

// Error is the shared structured error type.
type Error struct {
	Kind  Kind
	Stage string
	Track string
	Msg   string
	Err   error
}

func (e *Error) Error() string {
	s := "stage=" + e.Stage
	if e.Track != "" {
		s += " track=" + e.Track
	}
	s += ": " + e.Msg
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// Paramf builds a parameter/usage error (exit 2).
func Paramf(stage, format string, args ...any) *Error {
	return &Error{Kind: KindParam, Stage: stage, Msg: fmt.Sprintf(format, args...)}
}

// Corruptf builds a corrupt-asset or invalid-graph error (exit 3).
func Corruptf(stage, track, format string, args ...any) *Error {
	return &Error{Kind: KindCorrupt, Stage: stage, Track: track, Msg: fmt.Sprintf(format, args...)}
}

// Resumef builds a resume/checkpoint validation error (exit 4).
func Resumef(stage, format string, args ...any) *Error {
	return &Error{Kind: KindResume, Stage: stage, Msg: fmt.Sprintf(format, args...)}
}

// Wrap attaches stage/track context to a lower-level error.
func Wrap(k Kind, stage, track, msg string, err error) *Error {
	return &Error{Kind: k, Stage: stage, Track: track, Msg: msg, Err: err}
}

// KindOf extracts the Kind from any error, defaulting to internal.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindInternal
}

// ExitCode maps an error to the process exit code contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch KindOf(err) {
	case KindParam:
		return 2
	case KindCorrupt:
		return 3
	case KindResume:
		return 4
	default:
		return 1
	}
}
