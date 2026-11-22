// Package timeline defines the integer sample time base shared by every
// medpipe subsystem. All intervals are expressed in whole samples so that no
// floating point accumulation can drift across blocks, tracks or stages.
package timeline

import (
	"errors"
	"fmt"
)

// Stage identifiers annotate errors so a failure can be located to the exact
// subsystem that produced it. Values are stable strings used in manifests.
const (
	StageTimeline = "timeline"
	StageWAV      = "wav"
	StageRTP      = "rtp"
	StageResample = "resample"
	StageLayout   = "layout"
	StageLoudness = "loudness"
	StageQueue    = "queue"
	StageRunner   = "runner"
	StageManifest = "manifest"
)

// ErrorCode enumerates the failure classes the library can emit.
type ErrorCode string

const (
	ErrCodeTimestampBackwards ErrorCode = "timestamp_backwards"
	ErrCodeOutOfBounds        ErrorCode = "interval_out_of_bounds"
	ErrCodeOverlap            ErrorCode = "interval_overlap"
	ErrCodeDuplicateTimestamp ErrorCode = "duplicate_timestamp"
	ErrCodeTruncatedWAV       ErrorCode = "truncated_wav"
	ErrCodeIllegalBlockLength ErrorCode = "illegal_block_length"
	ErrCodeUnknownLayout      ErrorCode = "unknown_layout"
	ErrCodeSeqJump            ErrorCode = "rtp_sequence_jump"
	ErrCodeDurationMismatch   ErrorCode = "duration_mismatch"
	ErrCodeMissingBlock       ErrorCode = "missing_block"
	ErrCodeCycle              ErrorCode = "dependency_cycle"
	ErrCodeQueueClosed        ErrorCode = "queue_closed"
	ErrCodeCancelled          ErrorCode = "cancelled"
	ErrCodeCheckpoint         ErrorCode = "checkpoint_failure"
)

// Error carries the track, processing stage and machine-readable code of a
// failure. Every subsystem returns this type instead of a bare error string.
type Error struct {
	Code    ErrorCode
	Stage   string
	TrackID string
	Msg     string
}

func (e *Error) Error() string {
	if e.TrackID == "" {
		return fmt.Sprintf("%s: %s", e.Stage, e.Msg)
	}
	return fmt.Sprintf("%s[%s]: %s", e.Stage, e.TrackID, e.Msg)
}

// NewError builds a located error.
func NewError(code ErrorCode, stage, trackID, format string, args ...any) *Error {
	return &Error{Code: code, Stage: stage, TrackID: trackID, Msg: fmt.Sprintf(format, args...)}
}

// AsError unwraps a *Error from any error value.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
