// Package errors defines the unified error model shared by every
// subsystem: each failure carries its pipeline stage and the byte or
// sample offset at which it was detected.
package errors

import "fmt"

// Stage identifies the pipeline stage that raised an error.
type Stage string

const (
	StageIngest      Stage = "ingest"
	StageParse       Stage = "parse"
	StageVerify      Stage = "verify"
	StageReorder     Stage = "reorder"
	StageResample    Stage = "resample"
	StageLayout      Stage = "layout"
	StageLoudness    Stage = "loudness"
	StageJournal     Stage = "journal"
	StageCommit      Stage = "commit"
	StageRecover     Stage = "recover"
	StageFingerprint Stage = "fingerprint"
)

// Error is the single error type crossing module boundaries.
// Offset is a byte offset for container/parsing stages and a sample
// offset for audio stages; it is -1 when not applicable.
type Error struct {
	Stage  Stage
	Offset int64
	Op     string
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("stage=%s offset=%d op=%s: %v", e.Stage, e.Offset, e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// New wraps err with stage, offset and operation context.
func New(stage Stage, offset int64, op string, err error) *Error {
	return &Error{Stage: stage, Offset: offset, Op: op, Err: err}
}

// Newf builds an Error from a formatted message.
func Newf(stage Stage, offset int64, op, format string, args ...interface{}) *Error {
	return &Error{Stage: stage, Offset: offset, Op: op, Err: fmt.Errorf(format, args...)}
}

// StageOf extracts the pipeline stage from an error chain.
func StageOf(err error) (Stage, bool) {
	for err != nil {
		if e, ok := err.(*Error); ok {
			return e.Stage, true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return "", false
}

// OffsetOf extracts the recorded offset from an error chain.
func OffsetOf(err error) (int64, bool) {
	for err != nil {
		if e, ok := err.(*Error); ok {
			return e.Offset, true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return 0, false
}
