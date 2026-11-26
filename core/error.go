package core

import (
	"errors"
	"fmt"
)

// Kind classifies a structured Error by subsystem.
type Kind string

const (
	KindUsage      Kind = "usage"
	KindInput      Kind = "input"
	KindManifest   Kind = "manifest"
	KindPacket     Kind = "packet"
	KindInspect    Kind = "inspect"
	KindMigrate    Kind = "migrate"
	KindCheckpoint Kind = "checkpoint"
	KindVerify     Kind = "verify"
)

// Process exit codes required by the acceptance contract.
const (
	ExitOK         = 0
	ExitMismatch   = 1
	ExitUsage      = 2
	ExitCheckpoint = 75
)

// ErrStop signals an intentional checkpoint interrupt (--stop-after).
var ErrStop = errors.New("checkpoint stop requested")

// Error is the structured failure carried by every subsystem. Faults are
// reported, never silently repaired.
type Error struct {
	Kind   Kind
	Op     string // operation that failed, e.g. "parse-mpd"
	Path   string // file or resource, if any
	Offset int64  // byte offset if relevant, -1 otherwise
	Msg    string
	Err    error
}

// E builds an Error with no path/offset context.
func E(kind Kind, op, msg string, err error) *Error {
	return &Error{Kind: kind, Op: op, Offset: -1, Msg: msg, Err: err}
}

// At annotates the error with a path and byte offset.
func (e *Error) At(path string, offset int64) *Error {
	e.Path, e.Offset = path, offset
	return e
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: %s: %s", e.Kind, e.Op, e.Msg)
	if e.Path != "" {
		s += " path=" + e.Path
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps an error to the process exit code contract:
// 0 ok, 1 result mismatch / processing failure, 2 usage or input error,
// 75 intentional checkpoint stop.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, ErrStop) {
		return ExitCheckpoint
	}
	var e *Error
	if errors.As(err, &e) {
		switch e.Kind {
		case KindUsage, KindInput:
			return ExitUsage
		}
	}
	return ExitMismatch
}
