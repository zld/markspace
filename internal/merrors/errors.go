// Package merrors defines the shared error model used by every media
// subsystem. Every error carries the pipeline stage that produced it and,
// where meaningful, the byte offset or media timestamp at which the failure
// was detected.
package merrors

import (
	"errors"
	"fmt"
)

// Stage identifies the subsystem that produced an error.
type Stage string

const (
	StageRTPParse   Stage = "rtp.parse"
	StageRTPReorder Stage = "rtp.reorder"
	StageJitter     Stage = "buffer.jitter"
	StageFEC        Stage = "recovery.fec"
	StageTimeline   Stage = "clock.timeline"
	StageSync       Stage = "syncer.align"
	StageAdapter    Stage = "adapter.mp4v1"
	StageIndex      Stage = "index.keyframes"
	StageSegment    Stage = "segment.hash"
	StagePipeline   Stage = "pipeline.run"
	StageCommand    Stage = "cmd.mediadump"
)

// Category is a stable machine-readable classification of a failure.
type Category string

const (
	Truncated     Category = "truncated"
	Malformed     Category = "malformed"
	Bounds        Category = "bounds"
	Late          Category = "late"
	Duplicate     Category = "duplicate"
	Gap           Category = "gap"
	Unrecoverable Category = "unrecoverable"
	Checksum      Category = "checksum"
	Unsupported   Category = "unsupported"
	OutOfRange    Category = "out_of_range"
	Empty         Category = "empty"
	OutOfSync     Category = "out_of_sync"
	Overflow      Category = "overflow"
	Invalid       Category = "invalid"
)

// Error is the common error value. Offset is a byte offset into the input
// buffer, a media timestamp, or another subsystem-specific position.
type Error struct {
	Stage    Stage
	Category Category
	Offset   int64
	HasOff   bool
	SSRC     uint32
	Seq      uint16
	Message  string
	Err      error
}

// New builds an Error without a position.
func New(stage Stage, cat Category, format string, args ...any) *Error {
	return &Error{Stage: stage, Category: cat, Message: fmt.Sprintf(format, args...)}
}

// At builds an Error anchored at offset.
func At(stage Stage, cat Category, offset int64, format string, args ...any) *Error {
	return &Error{
		Stage: stage, Category: cat, Offset: offset, HasOff: true,
		Message: fmt.Sprintf(format, args...),
	}
}

// Packet builds an Error with RTP identity attached.
func Packet(stage Stage, cat Category, ssrc uint32, seq uint16, format string, args ...any) *Error {
	return &Error{
		Stage: stage, Category: cat, SSRC: ssrc, Seq: seq,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap attaches an Error envelope to an existing cause.
func Wrap(stage Stage, cat Category, offset int64, err error, format string, args ...any) *Error {
	return &Error{
		Stage: stage, Category: cat, Offset: offset, HasOff: true,
		Message: fmt.Sprintf(format, args...), Err: err,
	}
}

func (e *Error) Error() string {
	msg := string(e.Stage) + ": " + string(e.Category)
	if e.HasOff {
		msg += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.SSRC != 0 || e.Seq != 0 {
		msg += fmt.Sprintf(" ssrc=%d seq=%d", e.SSRC, e.Seq)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// CategoryOf returns the category of the first media error in err's chain.
func CategoryOf(err error) (Category, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me.Category, true
	}
	return "", false
}

// StageOf returns the stage of the first media error in err's chain.
func StageOf(err error) (Stage, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me.Stage, true
	}
	return "", false
}

// IsCategory reports whether err (or a wrapped cause) is the given category.
func IsCategory(err error, cat Category) bool {
	c, ok := CategoryOf(err)
	return ok && c == cat
}
