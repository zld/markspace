// Package merr defines the structured error model shared by all stages.
//
// Every processing failure carries the stage that observed it, the track it
// belongs to (0 when not track specific), and the byte or tick offset of the
// offending input. Nothing in the pipeline is silently corrected; callers can
// rely on an error always naming where and why the input was rejected.
package merr

import (
	"errors"
	"fmt"
)

// Stage names the pipeline phase an error was observed in.
type Stage string

const (
	StageFixture  Stage = "fixture"
	StageProbe    Stage = "probe"
	StageMetadata Stage = "metadata"
	StageCache    Stage = "cache"
	StageAudio    Stage = "audio"
	StageStream   Stage = "stream"
	StageTimeline Stage = "timeline"
	StageCheckpt  Stage = "checkpoint"
	StageManifest Stage = "manifest"
)

// AllStages lists the five acceptance stages in pipeline order.
var AllStages = []Stage{StageProbe, StageMetadata, StageAudio, StageStream, StageManifest}

// Kind classifies the error without parsing text.
type Kind string

const (
	KindParse        Kind = "parse"
	KindTruncated    Kind = "truncated"
	KindInconsistent Kind = "inconsistent"
	KindCorrupt      Kind = "corrupt"
	KindCanceled     Kind = "canceled"
	KindTimeout      Kind = "timeout"
	KindDigest       Kind = "digest"
	KindContinuity   Kind = "continuity"
	KindLimit        Kind = "limit"
	KindResume       Kind = "resume"
)

// Error is the structured processing error.
type Error struct {
	Stage       Stage
	TrackID     uint32
	InputOffset int64
	Kind        Kind
	Message     string
	Err         error
}

// New builds an Error.
func New(stage Stage, kind Kind, trackID uint32, inputOffset int64, format string, args ...interface{}) *Error {
	return &Error{
		Stage:       stage,
		Kind:        kind,
		TrackID:     trackID,
		InputOffset: inputOffset,
		Message:     fmt.Sprintf(format, args...),
	}
}

// Wrap builds an Error that wraps cause.
func Wrap(stage Stage, kind Kind, trackID uint32, inputOffset int64, cause error, format string, args ...interface{}) *Error {
	e := New(stage, kind, trackID, inputOffset, format, args...)
	e.Err = cause
	return e
}

func (e *Error) Error() string {
	track := "-"
	if e.TrackID != 0 {
		track = fmt.Sprintf("%d", e.TrackID)
	}
	msg := fmt.Sprintf("stage=%s kind=%s track=%s offset=%d: %s", e.Stage, e.Kind, track, e.InputOffset, e.Message)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// As extracts a structured error from err, if any.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// StageOf reports the stage of err, or "" when err is not structured.
func StageOf(err error) Stage {
	if e, ok := As(err); ok {
		return e.Stage
	}
	return ""
}
