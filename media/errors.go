package media

import (
	"errors"
	"fmt"
)

// Class is a stable, machine-readable error category shared by every
// subsystem so that callers can branch on failure kind without parsing text.
type Class string

const (
	ClassInvalidArgument Class = "invalid_argument"
	ClassIO              Class = "io"
	ClassTruncated       Class = "truncated"
	ClassBoxBounds       Class = "box_bounds"
	ClassSizeOverflow    Class = "size_overflow"
	ClassUnsupported     Class = "unsupported_box"
	ClassInvalidSample   Class = "invalid_sample"
	ClassMappingConflict Class = "mapping_conflict"
	ClassMissingTrack    Class = "missing_track"
	ClassTimebase        Class = "timebase"
	ClassCorruptPacket   Class = "corrupt_packet"
	ClassGap             Class = "gap"
	ClassCancelled       Class = "cancelled"
	ClassIntegrity       Class = "integrity"
)

// Error carries a stable class plus the structural context needed to locate
// the failure: byte offset, track id, segment index and RTP packet sequence.
type Error struct {
	Class    Class
	Op       string
	Offset   int64
	TrackID  uint32
	Segment  int
	Packet   uint64
	Message  string
	Wrapped  error
	hasLoc   bool
	hasTrack bool
	hasSeg   bool
	hasPkt   bool
}

// NewError builds a classified error at a byte offset.
func NewError(class Class, op string, offset int64, format string, args ...interface{}) *Error {
	return &Error{Class: class, Op: op, Offset: offset, hasLoc: true, Message: fmt.Sprintf(format, args...)}
}

// WrapError wraps a cause with a classified error at a byte offset.
func WrapError(class Class, op string, offset int64, cause error, format string, args ...interface{}) *Error {
	return &Error{Class: class, Op: op, Offset: offset, hasLoc: true, Wrapped: cause, Message: fmt.Sprintf(format, args...)}
}

// WithTrack returns a copy annotated with a track identifier.
func (e *Error) WithTrack(trackID uint32) *Error {
	cp := *e
	cp.TrackID = trackID
	cp.hasTrack = true
	return &cp
}

// WithSegment returns a copy annotated with a segment index.
func (e *Error) WithSegment(segment int) *Error {
	cp := *e
	cp.Segment = segment
	cp.hasSeg = true
	return &cp
}

// WithPacket returns a copy annotated with an RTP packet sequence.
func (e *Error) WithPacket(seq uint64) *Error {
	cp := *e
	cp.Packet = seq
	cp.hasPkt = true
	return &cp
}

func (e *Error) Error() string {
	msg := string(e.Class)
	if e.Op != "" {
		msg += ": " + e.Op
	}
	if e.hasLoc {
		msg += fmt.Sprintf(" at offset %d", e.Offset)
	}
	if e.hasTrack {
		msg += fmt.Sprintf(" track=%d", e.TrackID)
	}
	if e.hasSeg {
		msg += fmt.Sprintf(" segment=%d", e.Segment)
	}
	if e.hasPkt {
		msg += fmt.Sprintf(" packet=%d", e.Packet)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Wrapped != nil {
		msg += ": " + e.Wrapped.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Wrapped }

// HasLocation reports whether an offset was attached.
func (e *Error) HasLocation() bool { return e.hasLoc }

// ClassOf returns the class of err, or ClassIO for unclassified errors.
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ClassIO
}

// IsClass reports whether err belongs to the given class.
func IsClass(err error, class Class) bool {
	return err != nil && ClassOf(err) == class
}

// AsError extracts the classified error, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
