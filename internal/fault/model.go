// Package fault defines the shared error taxonomy and observable gap events
// used by RTP, MPEG-TS, demux, mux, and transaction stages.
package fault

import (
	"errors"
	"fmt"
)

// Exit codes required by the command-line acceptance contract.
const (
	ExitOK       = 0
	ExitCorrupt  = 2
	ExitInternal = 1
	ExitUsage    = 1
)

// Code is a stable, machine-readable fault identifier.
type Code string

const (
	OK                   Code = "ok"
	ErrUsage             Code = "usage_error"
	ErrInternal          Code = "internal_error"
	ErrInputTooLarge     Code = "input_too_large"
	ErrLogTooLarge       Code = "wal_too_large"
	ErrSyncByte          Code = "ts_bad_sync_byte"
	ErrPacketLength      Code = "ts_bad_packet_length"
	ErrPID               Code = "ts_bad_pid"
	ErrAdaptation        Code = "ts_bad_adaptation_field"
	ErrContinuity        Code = "ts_continuity_gap"
	ErrTruncated         Code = "truncated_input"
	ErrPCRWrap           Code = "pcr_wraparound"
	ErrPTSWrap           Code = "pts_wraparound"
	ErrBadCRC            Code = "psi_bad_crc"
	ErrPSISection        Code = "psi_missing_sections"
	ErrPSIVersion        Code = "psi_version_incomplete"
	ErrUnknownCodec      Code = "unknown_codec"
	ErrUnmappableTrack   Code = "unmappable_track"
	ErrCorruptSample     Code = "corrupt_media_sample"
	ErrMonotonic         Code = "non_monotonic_timestamp"
	ErrOutputUnreadable  Code = "output_unreadable"
	ErrDigestMismatch    Code = "digest_mismatch"
	ErrWAL               Code = "wal_error"
	ErrCommit            Code = "commit_error"
	ErrRollback          Code = "rollback_error"
	ErrResumeUnsupported Code = "resume_unsupported"
)

// TimeRangeMS is an inclusive-ish media interval in milliseconds.
type TimeRangeMS struct {
	Start int64 `json:"start_ms"`
	End   int64 `json:"end_ms"`
}

// Event describes a detected gap, wrap, version change, or corruption signal.
// Events are retained even when processing can continue so gaps are never
// silently removed from the report.
type Event struct {
	Code        Code         `json:"code"`
	Severity    string       `json:"severity"`
	Stage       string       `json:"stage,omitempty"`
	PID         uint16       `json:"pid,omitempty"`
	Offset      int64        `json:"offset,omitempty"`
	EndOffset   int64        `json:"end_offset,omitempty"`
	Sequence    uint16       `json:"sequence,omitempty"`
	HasSequence bool         `json:"has_sequence,omitempty"`
	TimeRange   *TimeRangeMS `json:"time_range_ms,omitempty"`
	Message     string       `json:"message,omitempty"`
	Recoverable bool         `json:"recoverable"`
}

// Error is an error with the stage, byte offset, and optional PID needed by
// the acceptance criteria.
type Error struct {
	Code      Code
	Stage     string
	Offset    int64
	EndOffset int64
	PID       uint16
	HasPID    bool
	Message   string
	Err       error
}

func (e *Error) Error() string {
	s := string(e.Code)
	if e.Stage != "" {
		s += " at " + e.Stage
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.HasPID {
		s += fmt.Sprintf(" pid=0x%04x", e.PID)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// New constructs a located fault.
func New(code Code, stage string, offset int64, msg string) *Error {
	return &Error{Code: code, Stage: stage, Offset: offset, Message: msg}
}

// Wrap wraps an error with a located fault code.
func Wrap(code Code, stage string, offset int64, msg string, err error) *Error {
	return &Error{Code: code, Stage: stage, Offset: offset, Message: msg, Err: err}
}

// WithPID attaches a PID and returns the same error for fluent construction.
func (e *Error) WithPID(pid uint16) *Error {
	e.PID = pid
	e.HasPID = true
	return e
}

// AsError converts arbitrary errors to *Error when possible.
func AsError(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// ExitCode maps corruption to 2 and all other failures to 1.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var fe *Error
	if errors.As(err, &fe) && IsCorruptCode(fe.Code) {
		return ExitCorrupt
	}
	return ExitInternal
}

// IsCorruptCode reports whether code represents bad input rather than an
// internal/transaction failure.
func IsCorruptCode(code Code) bool {
	switch code {
	case ErrSyncByte, ErrPacketLength, ErrPID, ErrAdaptation, ErrContinuity,
		ErrTruncated, ErrBadCRC, ErrPSISection, ErrPSIVersion,
		ErrUnknownCodec, ErrUnmappableTrack, ErrCorruptSample, ErrMonotonic,
		ErrOutputUnreadable, ErrDigestMismatch:
		return true
	default:
		return false
	}
}
