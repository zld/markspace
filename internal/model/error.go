// Package model defines the cross-module error model and pipeline stage
// state machine shared by every mediapack subsystem.
package model

import (
	"errors"
	"fmt"
)

// ErrorKind classifies failures so callers can branch on kind without parsing
// human-readable message text.
type ErrorKind string

const (
	// KindTruncated means a box or KLV item ends before its declared length.
	KindTruncated ErrorKind = "truncated"
	// KindOutOfRange means a declared length crosses container or file bounds.
	KindOutOfRange ErrorKind = "out_of_range"
	// KindUnsupported means a subset feature is not supported (e.g. layout).
	KindUnsupported ErrorKind = "unsupported"
	// KindConflict means metadata descriptions disagree (e.g. color).
	KindConflict ErrorKind = "conflict"
	// KindInvalidState means an illegal pipeline stage transition was asked.
	KindInvalidState ErrorKind = "invalid_state"
	// KindChecksum means a frame or manifest hash did not match.
	KindChecksum ErrorKind = "checksum"
	// KindGap means a representation layer has a missing packet sequence range.
	KindGap ErrorKind = "gap"
	// KindInvalidInput means input failed structural validation.
	KindInvalidInput ErrorKind = "invalid_input"
)

// SourceRef records where a metadata value was read from, so every retained
// field stays traceable to a file offset and structural field path.
type SourceRef struct {
	File   string `json:"file"`
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
}

// FieldError is the only structured error type parse/validation code returns.
// It always carries a file offset (-1 when not applicable) and a field path.
type FieldError struct {
	Kind    ErrorKind `json:"kind"`
	File    string    `json:"file"`
	Offset  int64     `json:"offset"`
	Path    string    `json:"path"`
	Message string    `json:"message"`
	Cause   error     `json:"-"`
}

// NewFieldError builds a FieldError. Offset is -1 when no byte offset applies.
func NewFieldError(kind ErrorKind, file string, offset int64, path, msg string) *FieldError {
	return &FieldError{Kind: kind, File: file, Offset: offset, Path: path, Message: msg}
}

// Truncated builds a truncation error at a concrete file offset.
func Truncated(file string, offset int64, path, msg string) *FieldError {
	return NewFieldError(KindTruncated, file, offset, path, msg)
}

// OutOfRange builds an out-of-bounds length error at a concrete file offset.
func OutOfRange(file string, offset int64, path, msg string) *FieldError {
	return NewFieldError(KindOutOfRange, file, offset, path, msg)
}

// WrapField attaches a cause to a new FieldError.
func WrapField(kind ErrorKind, file string, offset int64, path, msg string, cause error) *FieldError {
	return &FieldError{Kind: kind, File: file, Offset: offset, Path: path, Message: msg, Cause: cause}
}

func (e *FieldError) Error() string {
	loc := e.Path
	if e.Offset >= 0 {
		loc = fmt.Sprintf("%s@%d", e.Path, e.Offset)
	}
	if loc == "" {
		loc = "<root>"
	}
	s := fmt.Sprintf("%s: %s [%s]", e.File, loc, e.Message)
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause to errors.Is/errors.As.
func (e *FieldError) Unwrap() error { return e.Cause }

// AsField extracts the first *FieldError in err's chain.
func AsField(err error) (*FieldError, bool) {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// IsKind reports whether err contains a FieldError of the given kind.
func IsKind(err error, kind ErrorKind) bool {
	fe, ok := AsField(err)
	return ok && fe.Kind == kind
}
