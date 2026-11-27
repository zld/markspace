// Package fault defines the structured error model used by every pipeline
// stage. Every failure carries the processing stage, the track id, the
// byte offset inside the input where the failure was located (when known),
// and the frame or packet sequence number (when known).
package fault

import (
	"errors"
	"fmt"
)

// Stage identifiers. Stages are reported verbatim in CLI error output.
const (
	StageFrame       = "frame"
	StageSource      = "source"
	StageClock       = "clock"
	StageSegment     = "segment"
	StageSilence     = "silence"
	StageFingerprint = "fingerprint"
	StagePipeline    = "pipeline"
	StageCheckpoint  = "checkpoint"
	StageManifest    = "manifest"
	StageProvenance  = "provenance"
	StageVerify      = "verify"
)

// Error is the structured fault type propagated between stages.
type Error struct {
	// Stage is the subsystem that detected the fault.
	Stage string
	// Track is the track identifier, or "" for track-independent faults.
	Track string
	// Offset is the first locatable byte offset in the offending input.
	// A negative value means the offset is unknown.
	Offset int64
	// Frame is the frame/packet sequence number; negative when unknown.
	Frame int64
	// Op describes the operation that failed, e.g. "checksum".
	Op string
	// Err is the wrapped cause.
	Err error
}

// New builds a fault with unknown offset/frame markers.
func New(stage, op string, cause error) *Error {
	return &Error{Stage: stage, Op: op, Offset: -1, Frame: -1, Err: cause}
}

// AtTrack annotates the fault with a track id and returns it.
func (e *Error) AtTrack(track string) *Error { e.Track = track; return e }

// AtOffset annotates the fault with a locatable byte offset.
func (e *Error) AtOffset(off int64) *Error { e.Offset = off; return e }

// AtFrame annotates the fault with a frame/packet sequence number.
func (e *Error) AtFrame(n int64) *Error { e.Frame = n; return e }

func (e *Error) Error() string {
	s := e.Stage + ": " + e.Op
	if e.Track != "" {
		s += " track=" + e.Track
	}
	if e.Frame >= 0 {
		s += fmt.Sprintf(" frame=%d", e.Frame)
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause for errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a *fault.Error from err, wrapping it in a generic
// pipeline fault when err is not already structured.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var fe *Error
	if errors.As(err, &fe) {
		return fe
	}
	return New(StagePipeline, "process", err)
}

// Sentinel causes shared between packages.
var (
	ErrChecksum        = errors.New("checksum mismatch")
	ErrTruncated       = errors.New("truncated record")
	ErrLength          = errors.New("length field contradicts payload")
	ErrLimitExceeded   = errors.New("configured limit exceeded")
	ErrUnknownWindow   = errors.New("reorder window exhausted; gap marked unknown")
	ErrStaleCheckpoint = errors.New("checkpoint input/config digest mismatch")
)
