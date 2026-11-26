// Package fault defines the unified staged error model used by every
// mediamerge subsystem.
package fault

import "fmt"

// Stage identifies the processing stage that produced an error.
type Stage string

const (
	StageParse    Stage = "parse"
	StageClock    Stage = "clock"
	StageIndex    Stage = "index"
	StageRTP      Stage = "rtp"
	StageMap      Stage = "mapping"
	StageMerge    Stage = "merge"
	StageCAS      Stage = "cas"
	StageTxn      Stage = "txn"
	StageIO       Stage = "io"
	StageInternal Stage = "internal"
)

// Kind classifies an error so callers can map it to an exit code or policy.
type Kind string

const (
	KindEncoding      Kind = "invalid_encoding"
	KindTruncated     Kind = "truncated_cue"
	KindReversed      Kind = "reversed_interval"
	KindTimecode      Kind = "invalid_timecode"
	KindDropFrame     Kind = "invalid_dropframe"
	KindWrap          Kind = "timestamp_wrap"
	KindOverflow      Kind = "arithmetic_overflow"
	KindBounds        Kind = "bounds_violation"
	KindIndex         Kind = "index_incomplete"
	KindUnrecoverable Kind = "index_unrecoverable"
	KindRTP           Kind = "rtp_sequence"
	KindConflict      Kind = "merge_conflict"
	KindMissing       Kind = "missing_source"
	KindChecksum      Kind = "checksum_mismatch"
	KindState         Kind = "invalid_state"
	KindIO            Kind = "io_failure"
)

// Error is the structured error type shared by all packages.
type Error struct {
	Stage  Stage
	Kind   Kind
	Source string // file, track or other identifier (empty when unknown)
	Line   int    // 1-based input line, 0 when not applicable
	Detail string
}

func (e *Error) Error() string {
	loc := ""
	if e.Source != "" {
		loc = " source=" + e.Source
	}
	if e.Line > 0 {
		loc += fmt.Sprintf(" line=%d", e.Line)
	}
	return fmt.Sprintf("%s/%s:%s %s", e.Stage, e.Kind, loc, e.Detail)
}

// New builds an Error without a line number.
func New(stage Stage, kind Kind, source, detail string) *Error {
	return &Error{Stage: stage, Kind: kind, Source: source, Line: 0, Detail: detail}
}

// AtLine builds an Error attributed to a 1-based input line.
func AtLine(stage Stage, kind Kind, source string, line int, detail string) *Error {
	return &Error{Stage: stage, Kind: kind, Source: source, Line: line, Detail: detail}
}

// AtOffset builds an Error attributed to a byte offset.
func AtOffset(stage Stage, kind Kind, source string, offset int64, detail string) *Error {
	return &Error{Stage: stage, Kind: kind, Source: source,
		Detail: fmt.Sprintf("%s (offset=%d)", detail, offset)}
}

// As extracts a *Error from err, or returns nil.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	if fe, ok := err.(*Error); ok {
		return fe
	}
	type wrapper interface{ Unwrap() error }
	for {
		w, ok := err.(wrapper)
		if !ok {
			return nil
		}
		err = w.Unwrap()
		if fe, ok := err.(*Error); ok {
			return fe
		}
	}
}
