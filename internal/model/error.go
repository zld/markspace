// Package model defines the shared error classification used by every
// processing stage. Errors carry the pipeline stage that produced them, the
// PID and byte offset when applicable, and a severity that lets the driver
// decide whether recovery is possible.
package model

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline stage that emitted an error.
type Stage string

const (
	StageParse      Stage = "parse"
	StagePSI        Stage = "psi"
	StageContinuity Stage = "continuity"
	StageTimebase   Stage = "timebase"
	StageMetadata   Stage = "metadata"
	StagePipeline   Stage = "pipeline"
	StageIndex      Stage = "index"
	StageSegment    Stage = "segment"
)

// Severity splits errors into recoverable faults and fatal input failures.
type Severity int

const (
	// SeverityRecoverable means the affected packet or fragment can be
	// skipped or repaired; processing continues from the next safe boundary.
	SeverityRecoverable Severity = iota
	// SeverityFatal means the input cannot be interpreted safely at all.
	SeverityFatal
	// SeverityInternal signals an implementation fault rather than bad input.
	SeverityInternal
)

func (s Severity) String() string {
	switch s {
	case SeverityRecoverable:
		return "recoverable"
	case SeverityFatal:
		return "fatal"
	default:
		return "internal"
	}
}

// StreamError is the structured error type shared across modules.
type StreamError struct {
	Stage    Stage
	Severity Severity
	Message  string
	PID      uint16 // 0xFFFF when not applicable
	Offset   int64  // byte offset in the input stream; -1 when unknown
	Err      error  // wrapped cause, may be nil
}

// PIDNotApplicable marks an error that is not tied to a specific PID.
const PIDNotApplicable uint16 = 0xFFFF

// OffsetUnknown marks an error whose byte offset could not be determined.
const OffsetUnknown int64 = -1

func (e *StreamError) Error() string {
	pid := "n/a"
	if e.PID != PIDNotApplicable {
		pid = fmt.Sprintf("0x%04X", e.PID)
	}
	s := fmt.Sprintf("%s error (severity=%s, pid=%s, offset=%d): %s",
		e.Stage, e.Severity, pid, e.Offset, e.Message)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause for errors.Is/errors.As.
func (e *StreamError) Unwrap() error { return e.Err }

// New builds a StreamError without a PID/offset context.
func New(stage Stage, sev Severity, msg string) *StreamError {
	return &StreamError{Stage: stage, Severity: sev, Message: msg,
		PID: PIDNotApplicable, Offset: OffsetUnknown}
}

// At attaches PID and byte offset to an error. Pass PIDNotApplicable or
// OffsetUnknown to leave either dimension unset.
func (e *StreamError) At(pid uint16, offset int64) *StreamError {
	cp := *e
	cp.PID, cp.Offset = pid, offset
	return &cp
}

// Wrap attaches a cause while preserving the structured fields.
func (e *StreamError) Wrap(cause error) *StreamError {
	cp := *e
	cp.Err = cause
	return &cp
}

// Sentinel causes that callers may match with errors.Is.
var (
	ErrSyncByte         = errors.New("missing 0x47 sync byte")
	ErrPacketLength     = errors.New("buffer is not a full 188-byte TS packet")
	ErrTruncatedPacket  = errors.New("trailing bytes do not form a complete TS packet")
	ErrAdaptationLength = errors.New("adaptation field extends beyond packet")
	ErrPointerField     = errors.New("pointer field extends beyond payload")
	ErrCCCorrupt        = errors.New("payload present but continuity counter equals 0x0F discontinuity marker rules violated")
	ErrTimeRegression   = errors.New("timestamp moved backwards without a 33-bit wrap")
	ErrUnknownMapping   = errors.New("packet PID is absent from the active PAT/PMT mapping")
	ErrConflictingMap   = errors.New("PAT/PMT mapping conflict at version boundary")
	ErrUnknownLayout    = errors.New("unknown audio channel layout")
	ErrIndivisible      = errors.New("sample payload is not divisible by frame size")
	ErrColorConflict    = errors.New("undecidable color metadata conflict")
	ErrUnknownEncap     = errors.New("unknown encapsulation; only raw 188-byte TS and RTP-wrapped TS are accepted")
)

// AsStreamError extracts a *StreamError from err, returning nil otherwise.
func AsStreamError(err error) *StreamError {
	var se *StreamError
	if errors.As(err, &se) {
		return se
	}
	return nil
}

// IsFatal reports whether err is a fatal input-structure error.
func IsFatal(err error) bool {
	if se := AsStreamError(err); se != nil {
		return se.Severity == SeverityFatal
	}
	return false
}
