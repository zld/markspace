package clock

import (
	"errors"
	"fmt"
)

// Kind classifies every timeline/parse failure so callers can decide whether
// an input is merely recoverable or must abort the whole run.
type Kind string

const (
	KindInvalidMetadata  Kind = "invalid_metadata"
	KindTruncated        Kind = "truncated"
	KindOverflow         Kind = "overflow"
	KindNegativeDuration Kind = "negative_duration"
	KindOutOfRange       Kind = "out_of_range"
	KindNonMonotonic     Kind = "non_monotonic"
	KindUnsupported      Kind = "unsupported"
	KindMisaligned       Kind = "misaligned"
)

// Error carries a classification plus optional structural location so the CLI
// can print an error the operator can act on.
type Error struct {
	Kind      Kind
	TrackID   string
	Location  string
	Detail    string
	Err       error
	Recovered bool
}

// New creates a classified error with no location.
func New(kind Kind, detail string) *Error { return &Error{Kind: kind, Detail: detail} }

// Newf creates a classified error with formatted detail.
func Newf(kind Kind, format string, args ...interface{}) *Error {
	return &Error{Kind: kind, Detail: fmt.Sprintf(format, args...)}
}

// WithTrack attaches a track identifier.
func (e *Error) WithTrack(id string) *Error { e.TrackID = id; return e }

// WithLocation attaches a byte offset or logical marker.
func (e *Error) WithLocation(loc string) *Error { e.Location = loc; return e }

// WithCause attaches an underlying cause.
func (e *Error) WithCause(err error) *Error { e.Err = err; return e }

// MarkRecovered notes that processing continued despite the fault.
func (e *Error) MarkRecovered() *Error { e.Recovered = true; return e }

func (e *Error) Error() string {
	msg := string(e.Kind) + ": " + e.Detail
	if e.TrackID != "" {
		msg = "track " + e.TrackID + ": " + msg
	}
	if e.Location != "" {
		msg += " at " + e.Location
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the cause chain.
func (e *Error) Unwrap() error { return e.Err }

// AsKind extracts the classification of err, or "" when absent.
func AsKind(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// IsFatal reports whether an error should abort a run rather than isolate a
// single track or layer.
func IsFatal(err error) bool {
	if err == nil {
		return false
	}
	switch AsKind(err) {
	case KindTruncated, KindInvalidMetadata, KindOverflow, KindUnsupported:
		return true
	}
	return false
}
