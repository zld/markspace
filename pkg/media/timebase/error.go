package timebase

import (
	"errors"
	"fmt"
)

// Phase identifies the pipeline stage at which a problem was observed. The
// terminal renders it verbatim so that operators can tell a scan-time
// structural defect from a repair-time or clock-calibration defect.
type Phase string

// Pipeline phases, ordered roughly as data flows through the system.
const (
	PhaseScan       Phase = "scan"
	PhaseValidate   Phase = "validate"
	PhaseRepair     Phase = "repair"
	PhaseClock      Phase = "clock"
	PhaseManifest   Phase = "manifest"
	PhaseIndex      Phase = "index"
	PhaseCache      Phase = "cache"
	PhaseCheckpoint Phase = "checkpoint"
)

// Code is a stable, machine-readable error code. Codes never change between
// releases; new defects get new codes.
type Code string

// Defect codes. The E_ prefix marks hard defects; W_ marks warnings that do
// not block output (e.g. an isolated representation clock divergence).
const (
	CodeBoxLength        Code = "E_BOX_LENGTH"
	CodeReadOutOfBounds  Code = "E_READ_OUT_OF_BOUNDS"
	CodeMfhdDuplicate    Code = "E_MFHD_DUPLICATE"
	CodeMfhdGap          Code = "E_MFHD_GAP"
	CodeTfdtRollback     Code = "E_TFDT_ROLLBACK"
	CodeTfdtUnrepairable Code = "E_TFDT_UNREPAIRABLE"
	CodeMdatTruncated    Code = "E_MDAT_TRUNCATED"
	CodeClockDivergence  Code = "W_CLOCK_DIVERGENCE"
	CodeInputInvalid     Code = "E_INPUT_INVALID"
	CodeChecksumMismatch Code = "E_CHECKSUM_MISMATCH"
	CodeIO               Code = "E_IO"
)

// MediaError is the single structured defect type used across every package.
// Every field is optional except Code and Phase; zero values mean "unknown".
type MediaError struct {
	// Code is the stable machine-readable defect code.
	Code Code
	// Phase is the pipeline stage that produced the defect.
	Phase Phase
	// Representation is the rendition identifier (e.g. "video_90000").
	Representation string
	// Fragment is the fragment file name (base name) or "" for init segments.
	Fragment string
	// Box is the four-character box type when applicable (e.g. "mdat").
	Box string
	// ByteOffset is the absolute offset inside the fragment at which the
	// defect starts. -1 means "not positionable".
	ByteOffset int64
	// Msg carries human-readable detail.
	Msg string
	// Err wraps an underlying library/OS error, if any.
	Err error
}

// Error implements error.
func (e *MediaError) Error() string {
	s := fmt.Sprintf("%s phase=%s", e.Code, e.Phase)
	if e.Representation != "" {
		s += " rep=" + e.Representation
	}
	if e.Fragment != "" {
		s += " fragment=" + e.Fragment
	}
	if e.Box != "" {
		s += " box=" + e.Box
	}
	if e.ByteOffset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.ByteOffset)
	}
	if e.Msg != "" {
		s += " " + e.Msg
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped error for errors.Is/As.
func (e *MediaError) Unwrap() error { return e.Err }

// NewError builds a MediaError with an unknown byte offset.
func NewError(code Code, phase Phase, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Phase: phase, ByteOffset: -1, Msg: fmt.Sprintf(format, args...)}
}

// AtError builds a MediaError positioned at a byte offset.
func AtError(code Code, phase Phase, offset int64, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Phase: phase, ByteOffset: offset, Msg: fmt.Sprintf(format, args...)}
}

// WithContext returns a copy of the error annotated with representation and
// fragment identity. It is used when lower layers only know offsets.
func (e *MediaError) WithContext(rep, fragment string) *MediaError {
	cp := *e
	cp.Representation = rep
	cp.Fragment = fragment
	return &cp
}

// WithBox returns a copy annotated with a box type.
func (e *MediaError) WithBox(box string) *MediaError {
	cp := *e
	cp.Box = box
	return &cp
}

// AsMedia extracts a *MediaError from any error, wrapping plain errors in a
// generic IO record. Returns nil when err is nil.
func AsMedia(err error) *MediaError {
	if err == nil {
		return nil
	}
	var me *MediaError
	if errors.As(err, &me) {
		return me
	}
	return &MediaError{Code: CodeIO, Phase: PhaseScan, ByteOffset: -1, Msg: err.Error(), Err: err}
}
