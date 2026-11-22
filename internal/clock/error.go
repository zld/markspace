package clock

import (
	"context"
	"fmt"
)

// ErrorKind classifies every failure produced by the kernel so callers can
// pick a stable process exit code without string matching.
type ErrorKind int

const (
	// KindRecoverable: corrupt but bounded input (truncation, missing key
	// frame, edit-list out of bounds, unrepairable timestamp regression).
	// The pipeline keeps its checkpoints and may be re-run safely.
	KindRecoverable ErrorKind = iota + 1
	// KindInvalidArgument: bad CLI parameters or output path problems.
	KindInvalidArgument
	// KindInvariant: an internal cross-stage invariant was violated. This
	// must never happen on accepted input.
	KindInvariant
	// KindCanceled: the context ended (deadline or explicit cancellation)
	// while a bounded stream was blocked on a slow consumer.
	KindCanceled
)

// ExitCode maps an error kind to the documented process exit code:
// success=0, recoverable corruption=2, parameter error=3, invariant=4.
// A canceled run is treated as recoverable (checkpoint retained): exit 2.
func (k ErrorKind) ExitCode() int {
	switch k {
	case KindRecoverable, KindCanceled:
		return 2
	case KindInvalidArgument:
		return 3
	case KindInvariant:
		return 4
	default:
		return 4
	}
}

// MediaError is the single error type used across all subsystems.
type MediaError struct {
	Kind   ErrorKind
	Op     string // subsystem operation, e.g. "scan.stsz"
	Track  uint32 // 0 when not track-specific
	Sample int64  // -1 when not sample-specific
	Msg    string
}

func (e *MediaError) Error() string {
	if e.Track != 0 {
		return fmt.Sprintf("%s: track=%d sample=%d: %s", e.Op, e.Track, e.Sample, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Msg)
}

// Error implements Is so errors.Is(err, context.Canceled) still works for
// canceled runs.
func (e *MediaError) Is(target error) bool {
	if e.Kind == KindCanceled {
		return target == context.Canceled
	}
	return false
}

// ErrCanceled wraps a context end as a classified media error so every
// failure path carries an ErrorKind and an exit code.
func ErrCanceled(op string, err error) error {
	return &MediaError{Kind: KindCanceled, Op: op, Sample: -1, Msg: err.Error()}
}

// Classify returns the ErrorKind of err, or KindInvariant for plain errors
// that were not constructed through this package (defensive default).
func Classify(err error) ErrorKind {
	if err == nil {
		return 0
	}
	if me, ok := err.(*MediaError); ok {
		return me.Kind
	}
	return KindInvariant
}

// Constructors. Each subsystem prefixes Op with its own name.

func ErrRecoverablef(op, format string, args ...any) error {
	return &MediaError{Kind: KindRecoverable, Op: op, Sample: -1,
		Msg: fmt.Sprintf(format, args...)}
}

func ErrInvalidArgument(format string, args ...any) error {
	return &MediaError{Kind: KindInvalidArgument, Op: "argument", Sample: -1,
		Msg: fmt.Sprintf(format, args...)}
}

func ErrInvariantf(op, format string, args ...any) error {
	return &MediaError{Kind: KindInvariant, Op: op, Sample: -1,
		Msg: fmt.Sprintf(format, args...)}
}

func ErrOverflow(op string) error {
	return &MediaError{Kind: KindRecoverable, Op: op, Sample: -1,
		Msg: "integer overflow while computing box/sample bounds"}
}

// WithTrack annotates an error with track and sample position, preserving
// its classification. Re-annotating replaces the previous position.
func WithTrack(err error, track uint32, sample int64) error {
	me, ok := err.(*MediaError)
	if !ok {
		return err
	}
	cp := *me
	cp.Track, cp.Sample = track, sample
	return &cp
}
