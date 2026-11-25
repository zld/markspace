// Package diagnostic defines the reproducible error contract shared by every
// archive-check subsystem.
package diagnostic

import (
	"errors"
	"fmt"
)

// Phase identifies the processing stage that produced an error.
type Phase string

const (
	PhaseArguments Phase = "arguments"
	PhaseClock     Phase = "clock"
	PhaseSubtitle  Phase = "subtitle"
	PhaseProbe     Phase = "probe"
	PhaseLayout    Phase = "layout"
	PhaseRTP       Phase = "rtp"
	PhasePolicy    Phase = "policy"
	PhasePipeline  Phase = "pipeline"
	PhaseMerge     Phase = "merge"
	PhaseCancel    Phase = "cancel"
	PhaseReport    Phase = "report"
)

// Code is a stable, machine-readable failure class.
type Code string

const (
	CodeInvalidArgument     Code = "INVALID_ARGUMENT"
	CodeIllegalDropFrame    Code = "ILLEGAL_DROP_FRAME"
	CodeNegativeTime        Code = "NEGATIVE_TIME"
	CodeOverflow            Code = "OVERFLOW"
	CodeInvalidTimecode     Code = "INVALID_TIMECODE"
	CodeInvalidTimestamp    Code = "INVALID_TIMESTAMP"
	CodeTruncated           Code = "TRUNCATED_INPUT"
	CodeLineTooLarge        Code = "LINE_TOO_LARGE"
	CodeInputTooLarge       Code = "INPUT_TOO_LARGE"
	CodeTooManyItems        Code = "TOO_MANY_ITEMS"
	CodeMetadataConflict    Code = "METADATA_CONFLICT"
	CodeUnsupportedMetadata Code = "UNSUPPORTED_METADATA"
	CodeInvalidMP4          Code = "INVALID_MP4"
	CodeBadRTPTimestamp     Code = "BAD_RTP_TIMESTAMP"
	CodeInvalidRTP          Code = "INVALID_RTP"
	CodeBudgetInfeasible    Code = "BUDGET_INFEASIBLE"
	CodeCanceled            Code = "CANCELED"
	CodeQueueFull           Code = "QUEUE_FULL"
	CodeInternal            Code = "INTERNAL"
)

// Error carries the phase, sample number, and byte offset needed to locate a
// failure without guessing or silently repairing the source.
type Error struct {
	Phase   Phase  `json:"phase"`
	Sample  int    `json:"sample"`
	Offset  int64  `json:"offset"`
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

// New creates a structured error.
func New(code Code, phase Phase, sample int, offset int64, format string, args ...any) *Error {
	return &Error{
		Phase:   phase,
		Sample:  sample,
		Offset:  offset,
		Code:    code,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap creates a structured error around err.
func Wrap(code Code, phase Phase, sample int, offset int64, err error, format string, args ...any) *Error {
	return &Error{
		Phase:   phase,
		Sample:  sample,
		Offset:  offset,
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		Err:     err,
	}
}

func (e *Error) Error() string {
	msg := e.Message
	if e.Err != nil {
		if msg != "" {
			msg += ": " + e.Err.Error()
		} else {
			msg = e.Err.Error()
		}
	}
	return fmt.Sprintf("%s phase=%s sample=%d offset=%d: %s", e.Code, e.Phase, e.Sample, e.Offset, msg)
}

// Unwrap exposes the wrapped error.
func (e *Error) Unwrap() error { return e.Err }

// From finds the nearest structured error in err's chain.
func From(err error) (*Error, bool) {
	var de *Error
	if errors.As(err, &de) {
		return de, true
	}
	return nil, false
}

// HasCode reports whether err's chain contains code.
func HasCode(err error, code Code) bool {
	de, ok := From(err)
	return ok && de.Code == code
}
