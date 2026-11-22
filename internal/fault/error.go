// Package fault defines locatable, recoverable media-pipeline errors.
//
// Every error carries the file, byte offset, track and sample context needed
// to reproduce a failure, plus a recoverability flag that the CLI maps to its
// exit status (0 = clean, 1 = recovered input, 2 = verification failure).
package fault

import (
	"errors"
	"fmt"
)

// Kind classifies the processing stage that produced an error.
type Kind string

const (
	KindParse       Kind = "parse"
	KindTruncated   Kind = "truncated"
	KindTimeline    Kind = "timeline"
	KindIndex       Kind = "index"
	KindAudio       Kind = "audio"
	KindFingerprint Kind = "fingerprint"
	KindCheckpoint  Kind = "checkpoint"
	KindVerify      Kind = "verify"
	KindIO          Kind = "io"
)

// Sentinel causes. Callers can test them with errors.Is against wrapped errors.
var (
	ErrTruncated      = errors.New("file truncated")
	ErrBadBox         = errors.New("invalid box")
	ErrBadSample      = errors.New("invalid sample range")
	ErrTimeWrap       = errors.New("timestamp wrapped")
	ErrTimeBackwards  = errors.New("timestamp moved backwards")
	ErrTimeOverflow   = errors.New("timestamp overflow")
	ErrNoKeyframe     = errors.New("no sync frame in range")
	ErrIncomplete     = errors.New("incomplete index")
	ErrSilenceUnknown = errors.New("silence undetectable")
	ErrHashCollision  = errors.New("fingerprint collision")
	ErrTornRecord     = errors.New("torn checkpoint record")
)

// Error is a located error. Fields are populated progressively while a
// failure travels up the pipeline, so a parse error can be enriched with the
// track and sample that triggered it without losing the original cause.
type Error struct {
	Op          string // operation, e.g. "mp4.parse"
	Kind        Kind
	Path        string
	Offset      int64 // absolute byte offset in Path, -1 when unknown
	TrackID     uint32
	Sample      int // sample index, -1 when unknown
	Detail      string
	Recoverable bool
	Err         error
}

// New builds a located error with unknown location fields.
func New(op string, kind Kind, err error) *Error {
	return &Error{Op: op, Kind: kind, Offset: -1, Sample: -1, Err: err}
}

// Errorf builds a located error with a formatted detail.
func Errorf(op string, kind Kind, format string, args ...any) *Error {
	e := New(op, kind, nil)
	e.Detail = fmt.Sprintf(format, args...)
	return e
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := string(e.Kind)
	if e.Op != "" {
		s = e.Op + ": " + s
	}
	if e.Path != "" {
		s += fmt.Sprintf(" file=%q", e.Path)
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.TrackID != 0 {
		s += fmt.Sprintf(" track=%d", e.TrackID)
	}
	if e.Sample >= 0 {
		s += fmt.Sprintf(" sample=%d", e.Sample)
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	if e.Recoverable {
		s += " [recoverable]"
	}
	return s
}

// Unwrap exposes the sentinel cause for errors.Is.
func (e *Error) Unwrap() error { return e.Err }

// Is treats two located errors of the same kind and cause as equal so that
// tests and callers can use errors.Is with sentinel causes through the chain.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return e.Kind == t.Kind && errors.Is(e.Err, t.Err)
	}
	return false
}

// WithPath returns a copy carrying the file path.
func (e *Error) WithPath(path string) *Error { c := *e; c.Path = path; return &c }

// At returns a copy carrying an absolute byte offset.
func (e *Error) At(offset int64) *Error { c := *e; c.Offset = offset; return &c }

// WithTrack returns a copy carrying a track ID.
func (e *Error) WithTrack(id uint32) *Error { c := *e; c.TrackID = id; return &c }

// WithSample returns a copy carrying a sample index.
func (e *Error) WithSample(i int) *Error { c := *e; c.Sample = i; return &c }

// WithDetail returns a copy carrying formatted detail text.
func (e *Error) WithDetail(format string, args ...any) *Error {
	c := *e
	c.Detail = fmt.Sprintf(format, args...)
	return &c
}

// Recovered returns a copy marked recoverable.
func (e *Error) Recovered() *Error { c := *e; c.Recoverable = true; return &c }

// Fatal returns a copy marked non-recoverable.
func (e *Error) Fatal() *Error { c := *e; c.Recoverable = false; return &c }

// Wrapped returns a copy whose cause is err, preserving location fields.
func (e *Error) Wrapped(err error) *Error { c := *e; c.Err = err; return &c }

// AsRecovered converts any error into a recoverable located error, keeping
// the location fields already present in the chain.
func AsRecovered(err error, offset int64) *Error {
	if err == nil {
		return nil
	}
	if e, ok := As(err); ok {
		return e.Recovered()
	}
	return New("mp4", KindParse, err).At(offset).Recovered()
}

// As extracts the located error from an error chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// KindOf reports the kind of the first located error in the chain.
func KindOf(err error) Kind {
	if e, ok := As(err); ok {
		return e.Kind
	}
	return ""
}

// ExitCode maps an error to the CLI contract:
// 0 clean, 1 damaged input with recoverable results, 2 index/verify failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	e, ok := As(err)
	if !ok {
		return 2
	}
	switch e.Kind {
	case KindVerify:
		return 2
	case KindIndex:
		if !e.Recoverable {
			return 2
		}
	}
	if e.Recoverable {
		return 1
	}
	return 2
}
