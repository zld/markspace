// Package clock provides an exact time base and shared error model for the
// media continuity pipeline.
package clock

import (
	"errors"
	"fmt"
)

// Stage names the pipeline stage that detected a problem.
type Stage string

const (
	StageDemux      Stage = "demux"
	StageRepair     Stage = "repair"
	StageIndex      Stage = "index"
	StageCheckpoint Stage = "checkpoint"
	StageResume     Stage = "resume"
	StagePipeline   Stage = "pipeline"
	StageManifest   Stage = "manifest"
	StageReport     Stage = "report"
	StageMux        Stage = "mux"
)

// Sentinel errors are used by the command entry point to choose exit codes.
var (
	// ErrCorrupt marks input that cannot be interpreted safely.
	ErrCorrupt = errors.New("corrupt input")
	// ErrResumeMismatch marks a checkpoint that must not be resumed.
	ErrResumeMismatch = errors.New("resume state mismatch")
	// ErrGap marks a time that is not covered by available media.
	ErrGap = errors.New("time gap")
	// ErrCanceled marks an intentionally stopped run.
	ErrCanceled = errors.New("canceled")
	// ErrInterrupted marks the deterministic fault-injection stop.
	ErrInterrupted = errors.New("interrupted")
	// ErrOverflow marks exact rational arithmetic that cannot be represented.
	ErrOverflow = errors.New("rational overflow")
)

// Error adds pipeline context to an underlying error: stage, track, sample and
// operation. Track and Sample are only rendered when Known is set.
type Error struct {
	Stage  Stage
	Track  uint32
	Sample uint32
	Op     string
	KnownT bool
	KnownS bool
	Err    error
}

// NewError builds a contextual error.
func NewError(stage Stage, op string, err error) *Error {
	return &Error{Stage: stage, Op: op, Err: err}
}

// WithTrack records the track identifier on the error.
func (e *Error) WithTrack(id uint32) *Error {
	e.Track = id
	e.KnownT = true
	return e
}

// WithSample records a zero-based sample index on the error.
func (e *Error) WithSample(i uint32) *Error {
	e.Sample = i
	e.KnownS = true
	return e
}

// Error implements error.
func (e *Error) Error() string {
	msg := "stage=" + string(e.Stage)
	if e.KnownT {
		msg += fmt.Sprintf(" track=%d", e.Track)
	}
	if e.KnownS {
		msg += fmt.Sprintf(" sample=%d", e.Sample)
	}
	if e.Op != "" {
		msg += " op=" + e.Op
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the sentinel for errors.Is.
func (e *Error) Unwrap() error { return e.Err }

// Wrap decorates err with stage/op context unless it already carries a
// contextual Error for the same stage.
func Wrap(stage Stage, op string, err error) error {
	if err == nil {
		return nil
	}
	var ce *Error
	if errors.As(err, &ce) && ce.Stage == stage {
		return err
	}
	return NewError(stage, op, err)
}
