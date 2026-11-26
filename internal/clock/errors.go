package clock

import "fmt"

// ErrorKind classifies every failure produced by the pipeline so the CLI
// can pick an exit code and the manifest can record a stable reason code.
type ErrorKind string

const (
	// KindBoxTruncated: a box header or payload runs past EOF.
	KindBoxTruncated ErrorKind = "box_truncated"
	// KindBoxMalformed: a box parses but carries invalid values.
	KindBoxMalformed ErrorKind = "box_malformed"
	// KindTimeline: sample tables are inconsistent or missing.
	KindTimeline ErrorKind = "timeline_invalid"
	// KindRTP: an RTP packet could not be parsed.
	KindRTP ErrorKind = "rtp_bad"
	// KindAlignment: event time cannot be mapped onto the media clock.
	KindAlignment ErrorKind = "alignment_failed"
	// KindConflict: two events share an ID but carry different content.
	KindConflict ErrorKind = "event_conflict"
	// KindCheckpoint: a checkpoint or cached artifact failed verification.
	KindCheckpoint ErrorKind = "checkpoint_invalid"
	// KindConfig: bad command-line or pipeline configuration.
	KindConfig ErrorKind = "config_invalid"
)

// ClockError is the single structured error type shared by every
// subsystem. Kind drives machine classification while the remaining
// fields preserve where the problem happened (byte offset, track, event
// id ...) without leaking free-form strings into reports.
type ClockError struct {
	Kind      ErrorKind
	Op        string
	Reason    string
	Offset    int64 // byte offset for container problems; -1 when unused
	TrackID   uint32
	EventID   string
	Timescale uint32
	Detail    string
}

func (e *ClockError) Error() string {
	msg := string(e.Kind)
	if e.Op != "" {
		msg += " at " + e.Op
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Offset >= 0 {
		msg += fmt.Sprintf(" (byte_offset=%d)", e.Offset)
	}
	if e.TrackID != 0 {
		msg += fmt.Sprintf(" (track=%d)", e.TrackID)
	}
	if e.EventID != "" {
		msg += " (event=" + e.EventID + ")"
	}
	if e.Detail != "" {
		msg += " " + e.Detail
	}
	return msg
}

// WithOffset returns a copy annotated with a byte offset.
func (e *ClockError) WithOffset(off int64) *ClockError {
	cp := *e
	cp.Offset = off
	return &cp
}

// WithTrack returns a copy annotated with a track id.
func (e *ClockError) WithTrack(id uint32) *ClockError {
	cp := *e
	cp.TrackID = id
	return &cp
}

// WithEvent returns a copy annotated with an event id.
func (e *ClockError) WithEvent(id string) *ClockError {
	cp := *e
	cp.EventID = id
	return &cp
}

// NewError builds a structured error of the given kind.
func NewError(kind ErrorKind, op, reason string) *ClockError {
	return &ClockError{Kind: kind, Op: op, Reason: reason, Offset: -1}
}
