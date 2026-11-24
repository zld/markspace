// Package fault defines staged media processing errors that carry the byte
// offset of the first input byte that could not be parsed.
package fault

import (
	"errors"
	"fmt"
)

// Stage identifies the processing phase that failed.
type Stage string

const (
	StageProbe    Stage = "probe"
	StageDemux    Stage = "demux"
	StageRTP      Stage = "rtp"
	StageMap      Stage = "map"
	StageMetadata Stage = "metadata"
	StageRemux    Stage = "remux"
	StageValidate Stage = "validate"
)

// Operation is a short machine-readable description of the failing operation.
type Operation string

const (
	OpUnknownFormat    Operation = "unknown-format"
	OpBoxHeader        Operation = "box-header"
	OpBoxOverrun       Operation = "box-overrun"
	OpTruncated        Operation = "truncated"
	OpIncompleteHeader Operation = "incomplete-header"
	OpSequence         Operation = "sequence"
	OpIndexIncomplete  Operation = "index-incomplete"
	OpDemuxInvalid     Operation = "demux-invalid"
	OpMapping          Operation = "mapping"
	OpEvent            Operation = "event"
	OpWrite            Operation = "write"
	OpValidation       Operation = "validation"
)

// Error is a structured failure.  InputOffset is the zero-based byte position
// in the original input of the first byte that could not be parsed or verified.
// A value of -1 means that no byte offset applies (for example a track mapping
// error).
type Error struct {
	Stage       Stage
	Format      string
	Operation   Operation
	Reason      string
	InputOffset int64
	Cause       error
}

// New creates an Error.  offset must be a zero-based input byte offset, or -1
// when the failure is not attributable to input bytes.
func New(stage Stage, format string, op Operation, offset int64, reason string) *Error {
	return &Error{Stage: stage, Format: format, Operation: op, InputOffset: offset, Reason: reason}
}

// Wrap creates an Error around cause.
func Wrap(stage Stage, format string, op Operation, offset int64, reason string, cause error) *Error {
	return &Error{Stage: stage, Format: format, Operation: op, InputOffset: offset, Reason: reason, Cause: cause}
}

func (e *Error) Error() string {
	off := "unknown"
	if e.InputOffset >= 0 {
		off = fmt.Sprintf("%d", e.InputOffset)
	}
	msg := fmt.Sprintf("stage=%s operation=%s offset=%s", e.Stage, e.Operation, off)
	if e.Format != "" {
		msg += " format=" + e.Format
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Cause }

// HasOffset reports whether the error carries a meaningful input offset.
func (e *Error) HasOffset() bool { return e.InputOffset >= 0 }

// As extracts a structured Error from err.
func As(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// OffsetOf returns the input offset carried by err, if any.
func OffsetOf(err error) (int64, bool) {
	fe, ok := As(err)
	if !ok || !fe.HasOffset() {
		return 0, false
	}
	return fe.InputOffset, true
}

// IsStage reports whether err is a structured Error from stage.
func IsStage(err error, stage Stage) bool {
	fe, ok := As(err)
	return ok && fe.Stage == stage
}
