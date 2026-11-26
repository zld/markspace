package media

import (
	"errors"
	"fmt"
)

// Stage identifies the processing phase a failure occurred in. The strings
// are stable because they appear in CLI output and acceptance records.
type Stage string

const (
	StageScan        Stage = "scan"
	StageFingerprint Stage = "fingerprint"
	StagePlan        Stage = "plan"
	StageArchive     Stage = "archive"
	StageRecover     Stage = "recover"
)

// Exit codes returned by the mediad command. They are defined next to the
// error model so every package classifies failures consistently.
const (
	ExitOK              = 0
	ExitInvalidInput    = 2
	ExitArchiveFailure  = 3
	ExitInvariantFailed = 4
)

// Error is the shared structured error model used by ingestion and planning.
// Track is the track key when the failure is track-specific, else "".
// Stage is the pipeline phase and InputOffset is the byte offset within the
// offending input (or -1 when the failure is not byte-addressable).
type Error struct {
	Stage       Stage
	Track       string
	InputOffset int64
	Op          string
	Reason      string
	Cause       error
}

// NewError builds a structured error. InputOffset -1 means "not applicable".
func NewError(stage Stage, op, reason string) *Error {
	return &Error{Stage: stage, Op: op, Reason: reason, InputOffset: -1}
}

// WithTrack attaches a track key.
func (e *Error) WithTrack(track string) *Error { e.Track = track; return e }

// WithOffset attaches a byte offset within the input.
func (e *Error) WithOffset(off int64) *Error { e.InputOffset = off; return e }

// Wrap records the underlying cause.
func (e *Error) Wrap(cause error) *Error { e.Cause = cause; return e }

// Error renders a single human-readable, greppable line.
func (e *Error) Error() string {
	s := string(e.Stage) + ": " + e.Op + ": " + e.Reason
	if e.Track != "" {
		s += " track=" + e.Track
	}
	if e.InputOffset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.InputOffset)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap exposes the cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// AsError extracts a *media.Error from any error chain.
func AsError(err error) (*Error, bool) {
	var me *Error
	if ok := errors.As(err, &me); ok {
		return me, true
	}
	return nil, false
}

// ClassifyExit maps an error to the documented process exit code.
func ClassifyExit(err error) int {
	if err == nil {
		return ExitOK
	}
	me, ok := AsError(err)
	if !ok {
		return ExitInvariantFailed
	}
	switch me.Stage {
	case StageRecover, StageArchive:
		return ExitArchiveFailure
	default:
		return ExitInvalidInput
	}
}
