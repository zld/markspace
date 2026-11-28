// Package fault defines the single error model shared by every subsystem.
//
// Errors always carry enough context to point at a concrete track, RTP
// sequence number or file offset, so a regression failure can be traced back
// to the exact field that produced it.
package fault

import (
	"errors"
	"fmt"
)

// Kind classifies an error for the CLI exit-code mapping.
type Kind string

const (
	// KindCorrupt means the input container or stage artifact is damaged.
	// It maps to exit code 2.
	KindCorrupt Kind = "corrupt"
	// KindOverflow means integer rational arithmetic would overflow.
	KindOverflow Kind = "overflow"
	// KindInternal means an invariant inside the tool was violated.
	// It maps to exit code 1.
	KindInternal Kind = "internal"
)

// Error is the locatable fault type used across the project.
type Error struct {
	Kind       Kind
	Field      string // logical field name, e.g. "stco.entry[3].chunk_offset"
	TrackIndex int    // -1 when not track scoped
	Seq        uint16 // RTP sequence number, meaningful when HasSeq is true
	HasSeq     bool
	Offset     int64 // byte offset inside the input file, -1 when absent
	Msg        string
}

// New builds a fault without track/seq context. A negative offset means the
// fault is not tied to a byte position; zero is a valid file offset.
func New(kind Kind, field string, offset int64, format string, args ...interface{}) *Error {
	return &Error{
		Kind:       kind,
		Field:      field,
		TrackIndex: -1,
		Offset:     offset,
		Msg:        fmt.Sprintf(format, args...),
	}
}

// NoOffset is the sentinel passed to New when a fault has no byte position.
const NoOffset int64 = -1

// WithTrack attaches a track index and returns the same error for chaining.
func (e *Error) WithTrack(track int) *Error {
	e.TrackIndex = track
	return e
}

// WithSeq attaches an RTP sequence number.
func (e *Error) WithSeq(seq uint16) *Error {
	e.Seq = seq
	e.HasSeq = true
	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	s := string(e.Kind)
	if e.Field != "" {
		s += " at " + e.Field
	}
	if e.TrackIndex >= 0 {
		s += fmt.Sprintf(" track=%d", e.TrackIndex)
	}
	if e.HasSeq {
		s += fmt.Sprintf(" seq=%d", e.Seq)
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	return s + ": " + e.Msg
}

// AsError extracts a *Error from any wrapped error.
func AsError(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// IsCorrupt reports whether err is input corruption (exit code 2).
func IsCorrupt(err error) bool {
	if fe, ok := AsError(err); ok {
		return fe.Kind == KindCorrupt
	}
	return false
}
