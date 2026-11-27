package media

import (
	"errors"
	"fmt"
)

// Class is the stable category of a processing error. Categories are part of
// the output contract and are never rewritten by upper layers.
type Class string

const (
	ClassCorruptInput     Class = "corrupt_input"
	ClassTruncatedInput   Class = "truncated_input"
	ClassInvalidTimebase  Class = "invalid_timebase"
	ClassTimeRegression   Class = "time_regression"
	ClassOutOfRange       Class = "out_of_range"
	ClassInvalidCrop      Class = "invalid_crop"
	ClassUnsupported      Class = "unsupported"
	ClassDependency       Class = "dependency_incomplete"
	ClassCheckpoint       Class = "checkpoint_invalid"
	ClassOutputMismatch   Class = "output_mismatch"
	ClassInputChanged     Class = "input_changed"
	ClassSilenceTruncated Class = "silence_input_truncated"
)

// Stage names the pipeline step that produced a record. Output entries carry
// the stage so a manifest can be traced back to the exact producer.
type Stage string

const (
	StageParse       Stage = "parse"
	StageTimeline    Stage = "timeline"
	StageSplit       Stage = "split"
	StageSilence     Stage = "silence"
	StageFingerprint Stage = "fingerprint"
	StageCheckpoint  Stage = "checkpoint"
	StageProvenance  Stage = "provenance"
)

// Error is the structured error used across all subsystems. Offset is the
// first byte of the input that could not be interpreted, or -1 when the error
// is not tied to a file position.
type Error struct {
	Track   string `json:"track,omitempty"`
	Stage   Stage  `json:"stage"`
	Class   Class  `json:"class"`
	Offset  int64  `json:"offset"` // -1 means not applicable
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

// NewError builds a structured error with an explicit offset. A negative
// offset is normalised to -1 (unknown).
func NewError(class Class, stage Stage, track string, offset int64, message string) *Error {
	if offset < 0 {
		offset = -1
	}
	return &Error{Track: track, Stage: stage, Class: class, Offset: offset, Message: message}
}

// WrapError is NewError with an underlying cause preserved for errors.Is.
func WrapError(class Class, stage Stage, track string, offset int64, message string, cause error) *Error {
	e := NewError(class, stage, track, offset, message)
	e.Cause = cause
	return e
}

// Errorf is a formatting convenience.
func Errorf(class Class, stage Stage, track string, offset int64, format string, args ...any) *Error {
	return NewError(class, stage, track, offset, fmt.Sprintf(format, args...))
}

func (e *Error) Error() string {
	track := e.Track
	if track == "" {
		track = "-"
	}
	off := "?"
	if e.Offset >= 0 {
		off = fmt.Sprintf("%d", e.Offset)
	}
	msg := e.Message
	if msg == "" && e.Cause != nil {
		msg = e.Cause.Error()
	}
	return fmt.Sprintf("track=%s stage=%s class=%s offset=%s: %s", track, e.Stage, e.Class, off, msg)
}

// Unwrap exposes the cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// WithTrack returns a copy carrying the given track label. It is used when a
// parser learns the track identity after the underlying failure was recorded.
func (e *Error) WithTrack(track string) *Error {
	c := *e
	c.Track = track
	return &c
}

// IsClass reports whether err (or any wrapped error) has the given class.
func IsClass(err error, class Class) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Class == class
	}
	return false
}

// AsError extracts the first structured error in the chain.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// FirstOffset returns the smallest non-negative input offset among errs. It
// implements the "first unparseable byte wins" rule. ok is false if no error
// carries a position.
func FirstOffset(errs []*Error) (offset int64, first *Error, ok bool) {
	offset = -1
	for _, e := range errs {
		if e == nil || e.Offset < 0 {
			continue
		}
		if offset < 0 || e.Offset < offset {
			offset = e.Offset
			first = e
			ok = true
		}
	}
	return offset, first, ok
}
