// Package timeline provides the shared nanosecond timebase and the
// structured error type used by the manifest, index and txn subsystems.
package timeline

import (
	"errors"
	"fmt"
)

// Kind classifies structured errors shared by all subsystems.
type Kind string

const (
	KindMissingDuration Kind = "missing_duration"
	KindZeroTimescale   Kind = "zero_timescale"
	KindBadSequence     Kind = "bad_sequence"
	KindOverflow        Kind = "overflow"
	KindBackward        Kind = "backward_time"
	KindTruncated       Kind = "truncated_segment"
	KindDigestMismatch  Kind = "digest_mismatch"
	KindOverlap         Kind = "boundary_overlap"
	KindMissingRecord   Kind = "missing_record"
	KindInterrupted     Kind = "interrupted_stage"
)

// Error is the structured error type shared by every subsystem. It always
// carries the input source (and line when known) so failures are locatable.
type Error struct {
	Kind   Kind   `json:"kind"`
	Op     string `json:"op"`
	Source string `json:"source"`
	Line   int    `json:"line,omitempty"`
	Detail string `json:"detail"`
}

func (e *Error) Error() string {
	loc := e.Source
	if e.Line > 0 {
		loc = fmt.Sprintf("%s:%d", e.Source, e.Line)
	}
	return fmt.Sprintf("%s: %s at %s: %s", e.Op, e.Kind, loc, e.Detail)
}

// New builds a structured error. Inputs are never silently corrected;
// every rejection is reported with its origin.
func New(kind Kind, op, source string, line int, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Source: source, Line: line, Detail: fmt.Sprintf(format, args...)}
}

// As unwraps err into a *Error, reporting whether it was one.
func As(err error) (*Error, bool) {
	var te *Error
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}
