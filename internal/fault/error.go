// Package fault defines the structured error model shared by every stage.
//
// Every recoverable input problem is reported with the stage that found it and
// the byte offset in the input record stream (or -1 when the error is not tied
// to an offset). This lets the self-test print exact truncation offsets and
// lets the CLI classify failures into exit codes without string matching.
package fault

import (
	"errors"
	"fmt"
)

// Stage names a processing stage. Values are stable and appear in the manifest.
type Stage string

const (
	StageIngest   Stage = "ingest"
	StageRTP      Stage = "reorder"
	StageRTCP     Stage = "rtcp"
	StageEvents   Stage = "events"
	StageAlign    Stage = "align"
	StageMux      Stage = "mux"
	StageValidate Stage = "validate"
	StageManifest Stage = "manifest"
)

// Sentinel errors used for classification.
var (
	// ErrTruncated marks a record or field that ends before its declared size.
	ErrTruncated = errors.New("truncated input")
	// ErrLimit marks a violation of the fixed input or buffer budget.
	ErrLimit = errors.New("input limit exceeded")
	// ErrCorrupt marks bytes that cannot be interpreted by the stage.
	ErrCorrupt = errors.New("corrupt input")
	// ErrUnsupported marks a well-formed but unsupported variant.
	ErrUnsupported = errors.New("unsupported input")
	// ErrBudget marks a processing budget (time or memory) overrun.
	ErrBudget = errors.New("budget exceeded")
)

// Error carries the location of a stage failure.
type Error struct {
	Stage  Stage
	Op     string
	Offset int64 // byte offset in the input record stream, -1 if unknown
	Seq    int   // RTP sequence number, -1 if unknown
	Err    error
}

// New builds a structured error with an unknown offset and sequence number.
func New(stage Stage, op string, err error) *Error {
	return &Error{Stage: stage, Op: op, Offset: -1, Seq: -1, Err: err}
}

// At builds a structured error pinned to an input byte offset.
func At(stage Stage, op string, offset int64, err error) *Error {
	return &Error{Stage: stage, Op: op, Offset: offset, Seq: -1, Err: err}
}

// WithSeq returns a copy carrying an RTP sequence number.
func (e *Error) WithSeq(seq int) *Error {
	c := *e
	c.Seq = seq
	return &c
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	loc := ""
	if e.Offset >= 0 {
		loc = fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Seq >= 0 {
		loc += fmt.Sprintf(" seq=%d", e.Seq)
	}
	op := ""
	if e.Op != "" {
		op = " " + e.Op
	}
	return fmt.Sprintf("%s%s%s: %v", e.Stage, op, loc, e.Err)
}

// Unwrap exposes the sentinel for errors.Is.
func (e *Error) Unwrap() error { return e.Err }

// StageOf reports the stage of the first structured error in the chain.
func StageOf(err error) (Stage, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Stage, true
	}
	return "", false
}

// OffsetOf reports the input offset of the first structured error in the chain.
func OffsetOf(err error) (int64, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Offset, true
	}
	return 0, false
}

// Is reports whether err matches any of the given sentinels.
func Is(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}
