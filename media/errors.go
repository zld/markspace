package media

import "fmt"

// RecoveryPhase names where a structured error was produced. The pipeline
// uses it to decide whether a failure is diagnosable (corruption found), a
// parameter problem, or an internal recovery invariant failure.
type RecoveryPhase string

const (
	PhaseManifest   RecoveryPhase = "manifest"
	PhaseRead       RecoveryPhase = "read"
	PhaseContainer  RecoveryPhase = "container"
	PhaseTransport  RecoveryPhase = "transport"
	PhaseCache      RecoveryPhase = "cache"
	PhaseCheckpoint RecoveryPhase = "checkpoint"
	PhasePipeline   RecoveryPhase = "pipeline"
)

// Code classifies an error without parsing its message.
type Code string

const (
	CodeInput         Code = "input"
	CodeTruncated     Code = "truncated"
	CodeBoxOverflow   Code = "box_overflow"
	CodeMissingIndex  Code = "missing_index"
	CodeManifestDrift Code = "manifest_drift"
	CodePacketGap     Code = "packet_gap"
	CodePacketDup     Code = "packet_duplicate"
	CodeCachePoison   Code = "cache_poison"
	CodeCacheMiss     Code = "cache_miss"
	CodeCursorBack    Code = "cursor_backwards"
	CodeManifestChg   Code = "manifest_changed"
	CodeCanceled      Code = "canceled"
	CodeSlowConsumer  Code = "slow_consumer"
	CodeInternal      Code = "internal"
)

// Error is the structured error every package returns. It always carries a
// concrete offset when the failure concerns bytes, and the logical location
// (node, segment, track) when it concerns media structure.
type Error struct {
	Code      Code          `json:"code"`
	Phase     RecoveryPhase `json:"phase"`
	Node      string        `json:"node,omitempty"`
	Segment   int           `json:"segment,omitempty"`
	Track     uint32        `json:"track,omitempty"`
	Offset    int64         `json:"offset"`
	SegmentID string        `json:"segment_id,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	Err       error         `json:"-"`
}

// NewError builds a structured error with all locators defaulted.
func NewError(code Code, phase RecoveryPhase, detail string) *Error {
	return &Error{Code: code, Phase: phase, Offset: -1, Detail: detail}
}

// AtOffset records the byte offset the error refers to.
func (e *Error) AtOffset(off int64) *Error { e.Offset = off; return e }

// OnSegment records the input segment index and identifier.
func (e *Error) OnSegment(idx int, id string) *Error { e.Segment, e.SegmentID = idx, id; return e }

// OnNode records the edge node that produced or served the bytes.
func (e *Error) OnNode(node string) *Error { e.Node = node; return e }

// OnTrack records the track id.
func (e *Error) OnTrack(id uint32) *Error { e.Track = id; return e }

// Because attaches the wrapped cause.
func (e *Error) Because(err error) *Error { e.Err = err; return e }

// Error renders a stable, terminal-friendly line.
func (e *Error) Error() string {
	loc := fmt.Sprintf("%s/%s", e.Phase, e.Code)
	if e.Node != "" {
		loc += " node=" + e.Node
	}
	if e.SegmentID != "" || e.Segment > 0 {
		loc += fmt.Sprintf(" segment=%d(%s)", e.Segment, e.SegmentID)
	}
	if e.Track != 0 {
		loc += fmt.Sprintf(" track=%d", e.Track)
	}
	if e.Offset >= 0 {
		loc += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Detail != "" {
		loc += ": " + e.Detail
	}
	if e.Err != nil {
		loc += ": " + e.Err.Error()
	}
	return loc
}

// Unwrap exposes the cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.Err }

// HasCode reports whether err or anything it wraps is a *Error with code c.
func HasCode(err error, c Code) bool {
	for err != nil {
		if se, ok := err.(*Error); ok && se.Code == c {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// AsError extracts the outermost *Error.
func AsError(err error) (*Error, bool) {
	for err != nil {
		if se, ok := err.(*Error); ok {
			return se, true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		err = u.Unwrap()
	}
	return nil, false
}

// CorruptionError marks a diagnosable defect: the run may finish with exit
// status 3, but the defect location and recovery reason are reported.
type CorruptionError struct {
	Cause *Error
	Scope string
}

func (c *CorruptionError) Error() string { return c.Cause.Error() }
func (c *CorruptionError) Unwrap() error { return c.Cause }
