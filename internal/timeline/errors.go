// Package timeline defines the media time base shared by every subsystem and
// the structured error model that crosses module boundaries.
package timeline

import (
	"errors"
	"fmt"
	"strings"
)

// Stage identifies the subsystem that detected a failure. Stages are part of
// the machine-readable output so a failed run can be replayed and audited.
type Stage string

const (
	StageParse     Stage = "parse"
	StageValidate  Stage = "validate"
	StageNormalize Stage = "normalize"
	StageWrite     Stage = "write"
	StageCommit    Stage = "commit"
	StageResume    Stage = "resume"
	StageManifest  Stage = "manifest"
)

// Category is a stable failure taxonomy. Values are ASCII and never localized
// because tests and downstream tooling compare them verbatim.
type Category string

const (
	CatTruncated        Category = "truncated_element"
	CatIllegalLength    Category = "illegal_length"
	CatUnknownLength    Category = "unknown_length_boundary"
	CatLimitExceeded    Category = "limit_exceeded"
	CatUnsupported      Category = "unsupported_encoding"
	CatMissingElement   Category = "missing_element"
	CatDuplicate        Category = "duplicate_element"
	CatOrder            Category = "order_violation"
	CatTrackConflict    Category = "track_metadata_conflict"
	CatFragmentConflict Category = "fragment_violation"
	CatManifestVersion  Category = "manifest_version"
	CatManifestField    Category = "manifest_field"
	CatCheckpoint       Category = "checkpoint_mismatch"
	CatCheckpointState  Category = "checkpoint_state"
	CatIO               Category = "io_error"
)

// Exit codes are part of the public CLI contract.
const (
	ExitOK         = 0
	ExitBadInput   = 2
	ExitCheckpoint = 3
	ExitLocalIO    = 4
)

// Error is the single error type returned by all packages. Offset is always a
// raw byte offset into the input file (or checkpoint file) so callers can seek
// to the exact bytes that triggered the failure.
type Error struct {
	Stage     Stage
	Category  Category
	ElementID uint32
	Offset    int64
	Tracks    []uint64
	Field     string
	Reason    string
	Err       error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "stage=%s category=%s", e.Stage, e.Category)
	if e.ElementID != 0 {
		fmt.Fprintf(&b, " element=0x%X", e.ElementID)
	}
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if len(e.Tracks) > 0 {
		fmt.Fprintf(&b, " tracks=%v", e.Tracks)
	}
	if e.Field != "" {
		fmt.Fprintf(&b, " field=%s", e.Field)
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, " reason=%s", e.Reason)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, " cause=%v", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps a category onto the documented process exit status.
func (e *Error) ExitCode() int {
	switch e.Category {
	case CatCheckpoint, CatCheckpointState:
		return ExitCheckpoint
	case CatIO:
		return ExitLocalIO
	default:
		return ExitBadInput
	}
}

// WithTracks returns a copy carrying the related track numbers.
func (e *Error) WithTracks(tracks ...uint64) *Error {
	cp := *e
	cp.Tracks = append([]uint64(nil), tracks...)
	return &cp
}

// WithField returns a copy carrying a manifest field path.
func (e *Error) WithField(path string) *Error {
	cp := *e
	cp.Field = path
	return &cp
}

// New builds a structured error located at a raw input offset.
func New(stage Stage, cat Category, el uint32, off int64, format string, args ...interface{}) *Error {
	return &Error{
		Stage:     stage,
		Category:  cat,
		ElementID: el,
		Offset:    off,
		Reason:    fmt.Sprintf(format, args...),
	}
}

// Wrap builds a structured error that keeps the underlying cause.
func Wrap(err error, stage Stage, cat Category, el uint32, off int64, format string, args ...interface{}) *Error {
	e := New(stage, cat, el, off, format, args...)
	e.Err = err
	return e
}

// ExitCodeOf resolves the process exit status for any error. Unclassified
// errors are treated as bad input so a failure never exits 0 by accident.
func ExitCodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return ExitBadInput
}

// CategoryOf resolves the category of a structured error, or "" if unclassified.
func CategoryOf(err error) Category {
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return ""
}

// OffsetOf resolves the raw input offset, or -1 if the error carries none.
func OffsetOf(err error) int64 {
	var e *Error
	if errors.As(err, &e) {
		return e.Offset
	}
	return -1
}
