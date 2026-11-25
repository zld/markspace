// Package media holds the shared media-time model and the structured error
// type used by every stage of the offline regression pipeline.
package media

import (
	"errors"
	"fmt"
)

// ErrorKind classifies a failure so callers can map it to exit codes and to
// stable, greppable output without parsing messages.
type ErrorKind string

const (
	// KindInput marks malformed or truncated input fixtures.
	KindInput ErrorKind = "input"
	// KindUnsupported marks well-formed input the kernel deliberately refuses.
	KindUnsupported ErrorKind = "unsupported"
	// KindTimebase marks failures converting between track timebases.
	KindTimebase ErrorKind = "timebase"
	// KindBudget marks a planning problem: no candidate fits the constraints.
	KindBudget ErrorKind = "budget"
	// KindConflict marks an idempotency-key reuse with different inputs.
	KindConflict ErrorKind = "conflict"
	// KindResume marks checkpoint or manifest inconsistencies.
	KindResume ErrorKind = "resume"
	// KindVerify marks a completed artifact that failed independent checks.
	KindVerify ErrorKind = "verify"
	// KindInterrupt is used internally when a requested interrupt fires.
	KindInterrupt ErrorKind = "interrupt"
)

// Loc identifies where in the input a failure happened. Fields are optional,
// but presence is tracked explicitly where zero is a legitimate value: byte
// offset 0 is the first byte of the file and must still be reportable.
type Loc struct {
	// Track names the logical track, e.g. "audio", "video", "subtitle".
	Track string
	// Sample is the 1-based sample or packet index within the track.
	Sample int
	// Offset is the absolute byte offset in the primary input file.
	Offset int64
	// HasOffset distinguishes a real offset of 0 from "not recorded".
	HasOffset bool
	// Stage is the pipeline stage that observed the failure.
	Stage string
}

// String renders the location in a compact, deterministic order.
func (l Loc) String() string {
	s := ""
	add := func(v string) {
		if s != "" {
			s += " "
		}
		s += v
	}
	if l.Track != "" {
		add("track=" + l.Track)
	}
	if l.Sample > 0 {
		add(fmt.Sprintf("sample=%d", l.Sample))
	}
	if l.Stage != "" {
		add("stage=" + l.Stage)
	}
	if l.HasOffset {
		add(fmt.Sprintf("offset=%d", l.Offset))
	}
	if s == "" {
		return "at=<unknown>"
	}
	return s
}

// Error is the single structured error produced across the kernel. It always
// carries a kind, a message and an optional location, and it unwraps cleanly.
type Error struct {
	Kind ErrorKind
	Msg  string
	Loc  Loc
	Err  error
}

// Error implements the error interface. The format is stable so terminal
// output can be diffed between runs.
func (e *Error) Error() string {
	if e.Loc == (Loc{}) {
		return fmt.Sprintf("%s: %s", e.Kind, e.Msg)
	}
	return fmt.Sprintf("%s: %s (%s)", e.Kind, e.Msg, e.Loc.String())
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error {
	return e.Err
}

// New builds a structured error without a location.
func New(kind ErrorKind, msg string) *Error {
	return &Error{Kind: kind, Msg: msg}
}

// Newf builds a formatted structured error without a location.
func Newf(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// At attaches the missing parts of loc to err and returns the resulting
// *Error. Fields already recorded by the originating layer win, because they
// were observed closest to the failure. A non-structured error is wrapped
// with KindInput; a nil err yields nil.
func At(err error, loc Loc) *Error {
	if err == nil {
		return nil
	}
	var me *Error
	if !errors.As(err, &me) {
		return &Error{Kind: KindInput, Msg: err.Error(), Loc: loc, Err: err}
	}
	if me.Loc.Track == "" {
		me.Loc.Track = loc.Track
	}
	if me.Loc.Sample == 0 {
		me.Loc.Sample = loc.Sample
	}
	if me.Loc.Stage == "" {
		me.Loc.Stage = loc.Stage
	}
	if !me.Loc.HasOffset {
		me.Loc.Offset, me.Loc.HasOffset = loc.Offset, loc.HasOffset
	}
	return me
}

// KindOf reports the ErrorKind of err, defaulting to KindInput for foreign
// errors and "" for a nil error.
func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var me *Error
	if errors.As(err, &me) {
		return me.Kind
	}
	return KindInput
}

// IsKind reports whether err carries the given kind.
func IsKind(err error, kind ErrorKind) bool {
	return KindOf(err) == kind
}

// LocOf returns the location recorded on err, if any.
func LocOf(err error) (Loc, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me.Loc, true
	}
	return Loc{}, false
}
