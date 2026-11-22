// Package merr defines the single error model shared by every pipeline stage.
//
// Every error carries the stage that produced it, the logical stream it
// belongs to, a sequence number or byte offset when one is meaningful, and
// whether processing may resume after the faulty span. The error text is
// deterministic so repeated runs produce byte-identical reports.
package merr

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline phase that produced an error.
type Stage string

const (
	StageInput     Stage = "input"
	StageTransport Stage = "transport"
	StageReorder   Stage = "reorder"
	StageLoss      Stage = "loss"
	StageCodec     Stage = "codec"
	StageIndex     Stage = "index"
	StageTimeline  Stage = "timeline"
	StageTrack     Stage = "track"
	StageDemux     Stage = "demux"
	StageRemux     Stage = "remux"
	StagePipeline  Stage = "pipeline"
)

// Span is the explicit interval an error refers to. Offsets and sequence
// numbers are recorded as -1 when not applicable so that the zero value never
// collides with a legitimate value of 0.
type Span struct {
	Stream  int    `json:"stream"`
	Seq     int64  `json:"seq"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
	TrackID uint32 `json:"track_id,omitempty"`
}

// Error is the unified, structured error value used across the library.
type Error struct {
	Stage     Stage  `json:"stage"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Span      Span   `json:"span"`
	Recovered bool   `json:"recovered"`
	cause     error
}

// New builds an Error with no wrapped cause.
func New(stage Stage, code string, span Span, recovered bool, format string, args ...any) *Error {
	return &Error{
		Stage:     stage,
		Code:      code,
		Message:   fmt.Sprintf(format, args...),
		Span:      span,
		Recovered: recovered,
	}
}

// Wrap builds an Error that wraps cause.
func Wrap(stage Stage, code string, span Span, recovered bool, cause error, format string, args ...any) *Error {
	e := New(stage, code, span, recovered, format, args...)
	e.cause = cause
	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("%s/%s stream=%d seq=%d off=%d len=%d",
		e.Stage, e.Code, e.Span.Stream, e.Span.Seq, e.Span.Offset, e.Span.Length)
	if e.Span.TrackID != 0 {
		s += fmt.Sprintf(" track=%d", e.Span.TrackID)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Recovered {
		s += " (recoverable)"
	} else {
		s += " (fatal)"
	}
	return s
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// IsRecovered reports whether processing may resume after this error.
func (e *Error) IsRecovered() bool { return e != nil && e.Recovered }

// AsError extracts a *Error from err, or nil when err is not part of this model.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// NoSpan returns a Span that marks sequence and offset as not applicable.
func NoSpan(stream int) Span {
	return Span{Stream: stream, Seq: -1, Offset: -1}
}

// SeqSpan returns a Span anchored on a sequence number.
func SeqSpan(stream int, seq uint16) Span {
	return Span{Stream: stream, Seq: int64(seq), Offset: -1}
}

// OffsetSpan returns a Span anchored on a byte range.
func OffsetSpan(stream int, offset, length int64) Span {
	return Span{Stream: stream, Seq: -1, Offset: offset, Length: length}
}
