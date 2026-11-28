// Package media provides the shared time base and the located error model used
// by every archive-lab subsystem.
package media

import (
	"errors"
	"fmt"
	"strings"
)

// Stage names the pipeline phase in which a failure happened.
type Stage string

const (
	StageScan    Stage = "scan"
	StageParse   Stage = "parse"
	StageHash    Stage = "hash"
	StagePlan    Stage = "plan"
	StageExecute Stage = "execute"
	StageCache   Stage = "cache"
	StageReport  Stage = "report"
)

// Error is a located failure. Every field is optional except Stage and Op.
type Error struct {
	Stage   Stage
	Op      string
	Path    string
	Offset  int64 // byte offset inside Path, -1 when unknown
	Line    int   // 1-based manifest line, 0 when unknown
	Segment int   // 0-based segment index, -1 when unknown
	Detail  string
	Err     error
}

// New builds a located error without path information.
func New(stage Stage, op, format string, args ...interface{}) *Error {
	return &Error{Stage: stage, Op: op, Offset: -1, Segment: -1, Detail: fmt.Sprintf(format, args...)}
}

// Wrap attaches a located context to an existing error.
func Wrap(stage Stage, op string, err error, format string, args ...interface{}) *Error {
	e := New(stage, op, format, args...)
	e.Err = err
	return e
}

// WithPath records the file the error refers to.
func (e *Error) WithPath(path string) *Error {
	e.Path = path
	return e
}

// WithLine records a 1-based manifest line.
func (e *Error) WithLine(line int) *Error {
	e.Line = line
	return e
}

// WithOffset records a byte offset inside the input file.
func (e *Error) WithOffset(offset int64) *Error {
	e.Offset = offset
	return e
}

// WithSegment records a 0-based segment index.
func (e *Error) WithSegment(index int) *Error {
	e.Segment = index
	return e
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Stage))
	if e.Op != "" {
		b.WriteString("/")
		b.WriteString(e.Op)
	}
	b.WriteString(": ")
	if e.Path != "" {
		b.WriteString(e.Path)
	}
	if e.Offset >= 0 {
		fmt.Fprintf(&b, "@%d", e.Offset)
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, ":%d", e.Line)
	}
	if e.Segment >= 0 {
		fmt.Fprintf(&b, " [segment %d]", e.Segment)
	}
	if b.Len() > len(e.Stage)+2 && (e.Path != "" || e.Offset >= 0 || e.Line > 0 || e.Segment >= 0) {
		b.WriteString(": ")
	}
	b.WriteString(e.Detail)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// StageOf reports the stage of a located error, if err carries one.
func StageOf(err error) (Stage, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Stage, true
	}
	return "", false
}

// ExitCode maps an error to the CLI contract: 1 for usage/argument problems,
// 2 for input or processing failures.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if IsUsage(err) {
		return 1
	}
	return 2
}

// IsUsage reports whether err is a command-line usage problem (exit code 1).
// Argument flags are rejected during validation as well as during parsing.
func IsUsage(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		if e.Stage == StageScan && e.Op == "args" {
			return true
		}
	}
	return errors.Is(err, ErrUsage)
}

// ErrUsage marks errors that are the caller's mistake rather than an input
// or processing failure.
var ErrUsage = errors.New("usage error")

// Usage builds a usage error carrying exit code 1 semantics.
func Usage(format string, args ...interface{}) *Error {
	e := New(StageScan, "args", format, args...)
	e.Err = ErrUsage
	return e
}
