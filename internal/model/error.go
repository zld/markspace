// Package model defines the locatable error type carried through every stage
// of the pipeline so that failures always retain the file, track and byte
// offset where they originated, plus the processing stage that detected them.
package model

import (
	"errors"
	"fmt"
)

// Stage identifies a processing stage of a regression job.
type Stage string

const (
	StageReceive  Stage = "receive"  // bytes received / opened
	StageParse    Stage = "parse"    // media and events parsed
	StageArtifact Stage = "artifact" // evidence artifacts written
	StageCommit   Stage = "commit"   // manifest entry committed
)

// Error is a pipeline error that can be located down to a file, a track and
// a byte offset, and that records the stage that produced it. Zero offset or
// empty file/track mean the corresponding dimension is not applicable.
type Error struct {
	Stage  Stage  `json:"stage"`
	File   string `json:"file"`
	Track  string `json:"track,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Op     string `json:"op"`
	Err    error  `json:"-"`
}

// NewError builds a locatable stage error.
func NewError(stage Stage, op, file, track string, offset int64, cause error) *Error {
	return &Error{Stage: stage, Op: op, File: file, Track: track, Offset: offset, Err: cause}
}

func (e *Error) Error() string {
	loc := e.File
	if e.Track != "" {
		loc += "/" + e.Track
	}
	if e.Offset > 0 {
		loc = fmt.Sprintf("%s@%d", loc, e.Offset)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s at %s: %v", e.Stage, e.Op, loc, e.Err)
	}
	return fmt.Sprintf("%s: %s at %s", e.Stage, e.Op, loc)
}

// Unwrap exposes the wrapped cause for errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a *model.Error from err, or nil if err is not one.
func AsError(err error) *Error {
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return nil
}

// Sentinel failures that callers may match with errors.Is.
var (
	ErrTruncated   = errors.New("truncated input")
	ErrBadTimeBase = errors.New("illegal time base")
	ErrConflict    = errors.New("conflicting events at same tick")
	ErrCheckpoint  = errors.New("checkpoint failure")
	ErrRecovery    = errors.New("recovery verification failed")
)
