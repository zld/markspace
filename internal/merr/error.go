// Package merr defines the single unified error type used by every
// archivecheck subsystem. Errors carry a machine-readable code and a
// locator (manifest line, track, segment sequence, task key, ...) so a
// failure can always be traced back to the dependency that blocked the
// pipeline.
package merr

import (
	"errors"
	"fmt"
)

// Kind classifies an error independently of the subsystem that produced it.
type Kind string

const (
	KindManifest   Kind = "manifest_error"    // malformed or inconsistent playlist
	KindTransport  Kind = "transport_gap"     // packet loss outside the reorder window
	KindContainer  Kind = "unsupported_media" // codec/container outside the fixture subset
	KindDecode     Kind = "decode_error"      // truncated or undecodable frame payload
	KindTimeline   Kind = "timeline_error"    // clock conversion / alignment failure
	KindDAG        Kind = "dag_error"         // cycle, missing dependency, blocked node
	KindIO         Kind = "io_error"          // filesystem / atomic commit failure
	KindArgument   Kind = "argument_error"    // invalid command line parameters
	KindUnsupported Kind = "unsupported"
)

// Error is the unified error implementation shared by all packages.
type Error struct {
	Kind     Kind   `json:"kind"`
	Subsystem string `json:"subsystem"`
	Location string `json:"location"`
	Message  string `json:"message"`
	Cause    error  `json:"-"`
}

func (e *Error) Error() string {
	loc := e.Location
	if loc == "" {
		loc = "-"
	}
	msg := fmt.Sprintf("[%s/%s] %s: %s", e.Subsystem, loc, e.Kind, e.Message)
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Cause }

// New builds a unified error.
func New(kind Kind, subsystem, location, message string) *Error {
	return &Error{Kind: kind, Subsystem: subsystem, Location: location, Message: message}
}

// Wrap annotates a cause, preserving its Kind when it is already a *Error.
func Wrap(cause error, subsystem, location, message string) *Error {
	var existing *Error
	if errors.As(cause, &existing) {
		return &Error{
			Kind: existing.Kind, Subsystem: subsystem,
			Location: joinLoc(existing.Location, location),
			Message:  message + ": " + existing.Message, Cause: existing.Cause,
		}
	}
	return &Error{Kind: KindIO, Subsystem: subsystem, Location: location, Message: message, Cause: cause}
}

func joinLoc(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + ">" + b
	}
}

// As extracts a *Error from any wrapped error chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
