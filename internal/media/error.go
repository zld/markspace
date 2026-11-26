package media

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline phase in which an error occurred.
type Stage string

const (
	StageParseRTP   Stage = "ingest/rtp"
	StageParseMP4   Stage = "ingest/mp4"
	StageMeasure    Stage = "quality/measure"
	StagePlan       Stage = "quality/plan"
	StageSchedule   Stage = "dag/schedule"
	StageCheckpoint Stage = "checkpoint"
	StageProvenance Stage = "provenance"
	StagePipeline   Stage = "pipeline"
)

// ErrorKind lets callers distinguish corrupt inputs from task failures
// without parsing message text.
type ErrorKind string

const (
	KindCorrupt        ErrorKind = "corrupt_input"
	KindTruncated      ErrorKind = "truncated_box"
	KindBadLength      ErrorKind = "length_out_of_bounds"
	KindPathEscape     ErrorKind = "path_escape"
	KindUndetermined   ErrorKind = "duration_undetermined"
	KindPlanInfeasible ErrorKind = "plan_infeasible"
	KindDAGCycle       ErrorKind = "dag_cycle"
	KindCheckpoint     ErrorKind = "checkpoint_corrupt"
	KindVerify         ErrorKind = "verify_mismatch"
	KindUsage          ErrorKind = "usage"
)

// Error is the single error type shared by parsing and planning. It always
// carries the stage and, when an input position is known, the exact byte
// offset plus the track and extended RTP sequence number involved. Nothing
// is ever skipped silently: every parse failure becomes an Error.
type Error struct {
	Kind     ErrorKind
	Stage    Stage
	Track    string
	Sequence uint64 // extended RTP sequence number; 0 when not applicable
	Offset   int64  // byte offset inside the source file; -1 when unknown
	Detail   string
	Cause    error
}

// AtOffset marks the precise byte offset and returns the same error.
func (e *Error) AtOffset(off int64) *Error { e.Offset = off; return e }

// WithTrack attaches the track identifier and returns the same error.
func (e *Error) WithTrack(track string) *Error { e.Track = track; return e }

// WithSequence attaches the extended sequence number and returns.
func (e *Error) WithSequence(seq uint64) *Error { e.Sequence = seq; return e }

func (e *Error) Error() string {
	msg := string(e.Kind)
	if e.Stage != "" {
		msg += " at " + string(e.Stage)
	}
	if e.Track != "" {
		msg += " track=" + e.Track
	}
	if e.Sequence != 0 {
		msg += fmt.Sprintf(" seq=%d", e.Sequence)
	}
	if e.Offset >= 0 {
		msg += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap supports errors.Is/As against the wrapped cause.
func (e *Error) Unwrap() error { return e.Cause }

// NewError builds an Error with an unknown offset; use AtOffset when the
// input position is known.
func NewError(kind ErrorKind, stage Stage, detail string, cause error) *Error {
	return &Error{Kind: kind, Stage: stage, Offset: -1, Detail: detail, Cause: cause}
}

// AsError extracts a *media.Error from err, if present.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}
