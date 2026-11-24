// Package media defines the time base and error model shared by every
// subsystem (TS parser, RTP jitter buffer, audio pipeline, transactions).
package media

import "fmt"

// Class is the coarse category of a media error. Categories drive recovery
// decisions so callers never have to string-match error messages.
type Class string

const (
	ClassSync       Class = "sync"       // invalid sync byte / malformed packet
	ClassTruncated  Class = "truncated"  // packet cut short
	ClassContinuity Class = "continuity" // duplicate / gap / jump in counters
	ClassClock      Class = "clock"      // wrap, backwards time or drift fault
	ClassMetadata   Class = "metadata"   // PSI / track-map / color metadata fault
	ClassAudio      Class = "audio"      // resample / layout / loudness fault
	ClassRTP        Class = "rtp"        // RTP reorder / loss fault
	ClassValidation Class = "validation" // stage summary check failed
	ClassIO         Class = "io"         // journal / commit / filesystem fault
	ClassArg        Class = "arg"        // bad CLI argument / configuration
)

// Strategy is the recovery action associated with an error class.
type Strategy string

const (
	StrategyIgnore   Strategy = "ignore"   // safe to drop and continue
	StrategyRecover  Strategy = "recover"  // fill from window / resume from boundary
	StrategyRollback Strategy = "rollback" // abort stage, restore last committed state
	StrategyFatal    Strategy = "fatal"    // stop the pipeline
)

// MediaError is the single error type used across all subsystems.
type MediaError struct {
	Class    Class
	Strategy Strategy
	Op       string // operation / subsystem that detected the fault
	PID      uint16 // TS PID when applicable, otherwise 0xFFFF
	Index    int64  // packet / sequence index when applicable, else -1
	ByteOff  int64  // byte range start in the source stream, else -1
	ByteEnd  int64  // byte range end (exclusive), else -1
	Detail   string
	Err      error // wrapped cause, may be nil
}

func (e *MediaError) Error() string {
	s := fmt.Sprintf("media: class=%s strategy=%s op=%s", e.Class, e.Strategy, e.Op)
	if e.PID != 0xFFFF {
		s += fmt.Sprintf(" pid=0x%04X", e.PID)
	}
	if e.Index >= 0 {
		s += fmt.Sprintf(" index=%d", e.Index)
	}
	if e.ByteOff >= 0 {
		s += fmt.Sprintf(" bytes=[%d,%d)", e.ByteOff, e.ByteEnd)
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause for errors.Is / errors.As.
func (e *MediaError) Unwrap() error { return e.Err }

// New builds a MediaError, attaching the default recovery strategy of its class.
func New(class Class, op, detail string) *MediaError {
	return &MediaError{
		Class:    class,
		Strategy: DefaultStrategy(class),
		Op:       op,
		PID:      0xFFFF,
		Index:    -1,
		ByteOff:  -1,
		ByteEnd:  -1,
		Detail:   detail,
	}
}

// Wrap is New with a wrapped cause.
func Wrap(class Class, op string, cause error, detail string) *MediaError {
	e := New(class, op, detail)
	e.Err = cause
	return e
}

// WithPID attaches a PID and returns the receiver for chaining.
func (e *MediaError) WithPID(pid uint16) *MediaError { e.PID = pid; return e }

// WithIndex attaches a packet/sequence index.
func (e *MediaError) WithIndex(i int64) *MediaError { e.Index = i; return e }

// WithRange attaches a source byte range [off, end).
func (e *MediaError) WithRange(off, end int64) *MediaError {
	e.ByteOff, e.ByteEnd = off, end
	return e
}

// DefaultStrategy maps every error class to its recovery strategy.
func DefaultStrategy(c Class) Strategy {
	switch c {
	case ClassContinuity:
		return StrategyIgnore // duplicates are ignored; gaps may be recovered later
	case ClassTruncated, ClassRTP, ClassAudio, ClassSync, ClassMetadata:
		return StrategyRecover
	case ClassClock, ClassValidation:
		return StrategyRollback
	case ClassIO, ClassArg:
		return StrategyFatal
	default:
		return StrategyFatal
	}
}

// AsMedia extracts a *MediaError from any error, returning nil if absent.
func AsMedia(err error) *MediaError {
	if err == nil {
		return nil
	}
	if me, ok := err.(*MediaError); ok {
		return me
	}
	type unwrapper interface{ Unwrap() error }
	current := err
	for {
		u, ok := current.(unwrapper)
		if !ok {
			return nil
		}
		current = u.Unwrap()
		if current == nil {
			return nil
		}
		if me, ok := current.(*MediaError); ok {
			return me
		}
	}
}

// ClassOf returns the class of err, or "" when err is not a MediaError.
func ClassOf(err error) Class {
	if me := AsMedia(err); me != nil {
		return me.Class
	}
	return ""
}
