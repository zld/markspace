// Package model holds the shared vocabulary of the media timeline pipeline.
//
// Every failure that crosses a subsystem boundary is carried as an *Error so
// that callers can always tell which track failed, in which stage, and whether
// the pipeline may continue with a partial result.
package model

import (
	"errors"
	"fmt"
	"strings"
)

// Stage names the pipeline subsystem in which a failure was detected.
type Stage string

// Stages of the data flow. They are stable strings because they are printed
// in diagnostics and asserted by tests.
const (
	StageParse      Stage = "parse"
	StageClock      Stage = "clock-repair"
	StageMetadata   Stage = "metadata-recovery"
	StageAudio      Stage = "audio-recovery"
	StageVideo      Stage = "video-recovery"
	StageFanout     Stage = "fanout"
	StageFanin      Stage = "fanin"
	StageIndex      Stage = "index"
	StageValidation Stage = "validation"
)

// Error codes shared across the tree. Keeping them as constants makes the
// error-chain tests independent of human-readable text.
const (
	CodeBackwardTimestamp = "backward-timestamp"
	CodeClockOverflow     = "clock-overflow"
	CodeMalformedPacket   = "malformed-packet"
	CodeMalformedMP4      = "malformed-mp4"
	CodeMalformedMetadata = "malformed-metadata"
	CodeMissingPayload    = "missing-payload"
	CodeUnrecoverableLoss = "unrecoverable-loss"
	CodeAudioLayout       = "incompatible-audio-layout"
	CodeAudioRate         = "unsupported-sample-rate"
	CodeAudioGapOverflow  = "audio-gap-overflow"
	CodeColorUnknown      = "unknown-color-metadata"
	CodeBoundaryMismatch  = "segment-boundary-mismatch"
	CodeLayerUnavailable  = "layer-unavailable"
	CodeIndexInvalid      = "invalid-index"
	CodeEventMismatch     = "event-correspondence-mismatch"
	CodeLimitExceeded     = "limit-exceeded"
	CodeInvalidParameter  = "invalid-parameter"
)

// Error is the shared error type of the pipeline. Recoverable errors are
// recorded and repaired (the decision is kept in the output); unrecoverable
// errors abort the affected track or layer and are surfaced to the caller.
type Error struct {
	Track       TrackID
	Stage       Stage
	Code        string
	Recoverable bool
	Message     string
	Err         error
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "track=%s stage=%s code=%s recoverable=%t", blankTrack(e.Track), e.Stage, e.Code, e.Recoverable)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

func blankTrack(t TrackID) string {
	if t == "" {
		return "-"
	}
	return string(t)
}

// NewError builds a shared pipeline error.
func NewError(track TrackID, stage Stage, code string, recoverable bool, format string, args ...interface{}) *Error {
	return &Error{
		Track:       track,
		Stage:       stage,
		Code:        code,
		Recoverable: recoverable,
		Message:     fmt.Sprintf(format, args...),
	}
}

// WrapError builds a shared pipeline error around a cause.
func WrapError(track TrackID, stage Stage, code string, recoverable bool, cause error, format string, args ...interface{}) *Error {
	e := NewError(track, stage, code, recoverable, format, args...)
	e.Err = cause
	return e
}

// AsError returns the *Error in err's chain, if any.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// IsRecoverable reports whether err is a shared recoverable error. A plain
// error (one that was never classified) is treated as unrecoverable.
func IsRecoverable(err error) bool {
	if err == nil {
		return true
	}
	pe, ok := AsError(err)
	if !ok {
		return false
	}
	return pe.Recoverable
}

// ErrorList collects classified errors while preserving order. It is the
// deterministic record behind the "修复决策" output.
type ErrorList struct {
	Items []*Error
}

// Add appends an error and returns it.
func (l *ErrorList) Add(err *Error) *Error {
	if err != nil {
		l.Items = append(l.Items, err)
	}
	return err
}

// Addf appends a freshly built error.
func (l *ErrorList) Addf(track TrackID, stage Stage, code string, recoverable bool, format string, args ...interface{}) *Error {
	return l.Add(NewError(track, stage, code, recoverable, format, args...))
}

// Len returns the number of collected errors.
func (l *ErrorList) Len() int { return len(l.Items) }

// Unrecoverable returns the first unrecoverable error, or nil.
func (l *ErrorList) Unrecoverable() *Error {
	for _, e := range l.Items {
		if !e.Recoverable {
			return e
		}
	}
	return nil
}

// Recoverable returns only the recoverable errors, in insertion order.
func (l *ErrorList) Recoverable() []*Error {
	out := make([]*Error, 0, len(l.Items))
	for _, e := range l.Items {
		if e.Recoverable {
			out = append(out, e)
		}
	}
	return out
}

// Messages renders the list deterministically for diagnostics.
func (l *ErrorList) Messages() []string {
	out := make([]string, 0, len(l.Items))
	for _, e := range l.Items {
		out = append(out, e.Error())
	}
	return out
}
