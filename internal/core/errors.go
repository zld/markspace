// Package core holds the structured error vocabulary and exit-code policy
// shared by every mediacore subsystem.
package core

import (
	"errors"
	"fmt"
	"strings"
)

// Code is a stable, machine-readable failure class. It is written to the
// manifest and used by the CLI to pick an exit status.
type Code string

const (
	CodeIO              Code = "io"
	CodeMoovMissing     Code = "moov_missing"
	CodeBoxTruncated    Code = "box_truncated"
	CodeTableTruncated  Code = "table_truncated"
	CodeSampleRange     Code = "sample_range"
	CodeBadTimestamp    Code = "bad_timestamp"
	CodeCorruptFragment Code = "corrupt_fragment"
	CodeUnsupported     Code = "unsupported_codec"
	CodeMissingTrack    Code = "missing_track"
	CodeDependencyCycle Code = "dependency_cycle"
	CodeParamConflict   Code = "parameter_conflict"
	CodeUnrecoverable   Code = "unrecoverable"
	CodeLimitExceeded   Code = "limit_exceeded"
)

// Stage identifies the subsystem that found the problem.
type Stage string

const (
	StageScan      Stage = "scan"
	StageRecover   Stage = "recover"
	StageGraph     Stage = "graph"
	StageAudio     Stage = "audio"
	StagePacketize Stage = "packetize"
	StageManifest  Stage = "manifest"
)

// Error carries a location (byte offset, track, sample) next to every failure
// so callers never have to re-derive it from free-form text.
type Error struct {
	Code        Code
	Stage       Stage
	File        string
	Offset      int64
	Track       uint32
	Sample      int
	Node        string
	Recoverable bool
	Detail      string
	Err         error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("stage=")
	b.WriteString(string(e.Stage))
	b.WriteString(" code=")
	b.WriteString(string(e.Code))
	if e.File != "" {
		fmt.Fprintf(&b, " file=%s", e.File)
	}
	if e.Offset > 0 {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if e.Track != 0 {
		fmt.Fprintf(&b, " track=%d", e.Track)
	}
	if e.Sample != 0 {
		fmt.Fprintf(&b, " sample=%d", e.Sample)
	}
	if e.Node != "" {
		fmt.Fprintf(&b, " node=%s", e.Node)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// New builds a structured error. Recoverable errors are retained in the
// manifest; fatal errors stop the pipeline.
func New(code Code, stage Stage, recoverable bool, detail string) *Error {
	return &Error{Code: code, Stage: stage, Recoverable: recoverable, Detail: detail}
}

// Wrap adds a cause without losing the structured fields.
func Wrap(code Code, stage Stage, recoverable bool, detail string, err error) *Error {
	e := New(code, stage, recoverable, detail)
	e.Err = err
	return e
}

// WithOffset returns a copy located at a byte offset.
func (e *Error) WithOffset(off int64) *Error {
	c := *e
	c.Offset = off
	return &c
}

// WithTrack returns a copy located at a track and sample.
func (e *Error) WithTrack(track uint32, sample int) *Error {
	c := *e
	c.Track = track
	c.Sample = sample
	return &c
}

// WithNode returns a copy located at a dependency-graph node.
func (e *Error) WithNode(node string) *Error {
	c := *e
	c.Node = node
	return &c
}

// IsCode reports whether err (or anything it wraps) is a core error with code.
func IsCode(err error, code Code) bool {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Code == code
	}
	return false
}

// As returns the structured error at the head of err's chain, if any.
func As(err error) (*Error, bool) {
	var ce *Error
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// ExitCode maps errors to the CLI contract: graph cycles are 2, input that
// cannot be safely recovered is 3, everything else is an internal failure (1).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	ce, ok := As(err)
	if !ok {
		return 1
	}
	switch ce.Code {
	case CodeDependencyCycle:
		return 2
	case CodeUnrecoverable, CodeMissingTrack:
		return 3
	default:
		return 1
	}
}
