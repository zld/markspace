package clock

import (
	"errors"
	"fmt"
)

// ErrorKind classifies structured errors produced across subsystems so the
// CLI can map them deterministically to exit codes.
type ErrorKind string

const (
	KindInvalidInput  ErrorKind = "invalid_input"  // bad manifests / references / paths -> exit 2
	KindOverflow      ErrorKind = "overflow"       // integer clock overflow -> exit 2
	KindUnrecoverable ErrorKind = "unrecoverable"  // gap that no backup can fill -> exit 2
	KindHashConflict  ErrorKind = "hash_conflict"  // same path, different content -> exit 2
	KindCorruptMedia  ErrorKind = "corrupt_media"  // truncated/invalid fMP4 -> exit 2
	KindInjectedCrash ErrorKind = "injected_crash" // fault injection -> exit 75
	KindIO            ErrorKind = "io"             // local filesystem failure
)

// MediaError is the structured error type every subsystem returns. Location
// carries file/line or byte-offset context; Kind drives the exit code.
type MediaError struct {
	Kind     ErrorKind
	File     string
	Line     int    // 1-based; 0 when not applicable
	Offset   int64  // byte offset; -1 when not applicable
	Op       string // short operation label, e.g. "parse dash"
	Message  string
	Wrap     error
}

func (e *MediaError) Error() string {
	loc := ""
	if e.File != "" {
		loc = e.File
		if e.Line > 0 {
			loc += fmt.Sprintf(":%d", e.Line)
		} else if e.Offset >= 0 {
			loc += fmt.Sprintf("@%d", e.Offset)
		}
		loc += ": "
	}
	op := ""
	if e.Op != "" {
		op = e.Op + ": "
	}
	msg := e.Message
	if e.Wrap != nil {
		msg = e.Message + ": " + e.Wrap.Error()
	}
	return string(e.Kind) + ": " + loc + op + msg
}

func (e *MediaError) Unwrap() error { return e.Wrap }

// OverflowError marks an integer rational conversion overflow.
type OverflowError struct {
	Detail string
}

func (e *OverflowError) Error() string { return "clock overflow: " + e.Detail }

func overflowf(format string, args ...any) error {
	return &OverflowError{Detail: fmt.Sprintf(format, args...)}
}

// Sentinel errors.
var (
	ErrZeroDenominator = &MediaError{
		Kind:    KindOverflow,
		Message: "zero denominator in rational clock",
	}
	ErrInvalidRat = &MediaError{
		Kind:    KindInvalidInput,
		Message: "operation on uninitialized Rat",
	}
)

// New builds a MediaError with a kind and formatted message.
func New(kind ErrorKind, op, format string, args ...any) *MediaError {
	return &MediaError{Kind: kind, Op: op, Offset: -1, Message: fmt.Sprintf(format, args...)}
}

// AtFile attaches a file location to an error, preserving its type.
func AtFile(err error, file string) error {
	var me *MediaError
	if errors.As(err, &me) {
		cp := *me
		cp.File = file
		return &cp
	}
	return err
}

// AtLine attaches a 1-based line number.
func AtLine(err error, line int) error {
	var me *MediaError
	if errors.As(err, &me) {
		cp := *me
		cp.Line = line
		return &cp
	}
	return err
}

// AtOffset attaches a byte offset.
func AtOffset(err error, off int64) error {
	var me *MediaError
	if errors.As(err, &me) {
		cp := *me
		cp.Offset = off
		return &cp
	}
	return err
}

// KindOf extracts the ErrorKind of a structured error; unknown -> KindIO.
func KindOf(err error) ErrorKind {
	var me *MediaError
	if errors.As(err, &me) {
		return me.Kind
	}
	var oe *OverflowError
	if errors.As(err, &oe) {
		return KindOverflow
	}
	return KindIO
}

// AsMedia returns the structured form of err, or nil.
func AsMedia(err error) *MediaError {
	var me *MediaError
	if errors.As(err, &me) {
		return me
	}
	return nil
}
