package model

import (
	"errors"
	"fmt"
)

// Stage identifies the subsystem that produced a MediaError.
type Stage string

const (
	StageDemux    Stage = "demux"
	StageMap      Stage = "track-map"
	StageTimeline Stage = "timeline"
	StageRTP      Stage = "rtp"
	StageRemux    Stage = "remux"
	StageVerify   Stage = "verify"
	StageCLI      Stage = "cli"
)

// ErrorCode is a stable, machine-readable failure class.
type ErrorCode string

const (
	CodeTruncated       ErrorCode = "truncated_container"
	CodeUnsupported     ErrorCode = "unsupported_structure"
	CodeDuplicateTarget ErrorCode = "duplicate_target"
	CodeMissingRequired ErrorCode = "missing_required_track"
	CodeLayoutMismatch  ErrorCode = "channel_layout_mismatch"
	CodeRateMismatch    ErrorCode = "sample_rate_mismatch"
	CodeColorConflict   ErrorCode = "color_metadata_conflict"
	CodeLossOverWindow  ErrorCode = "loss_over_recovery_window"
	CodeLateBoundary    ErrorCode = "late_event_boundary"
	CodeNonLossless     ErrorCode = "non_lossless_transform"
	CodeOutputExists    ErrorCode = "output_exists"
	CodeInvalidArgument ErrorCode = "invalid_argument"
	CodeCorruptPayload  ErrorCode = "corrupt_payload"
)

// MediaError carries the stage, optional track, media timestamp and source
// byte offset at which a failure was detected.
type MediaError struct {
	Stage         Stage      `json:"stage"`
	Code          ErrorCode  `json:"code"`
	Message       string     `json:"message"`
	TrackID       *uint32    `json:"track_id,omitempty"`
	Timestamp     *Timestamp `json:"timestamp,omitempty"`
	SourceOffset  *int64     `json:"source_offset,omitempty"`
	Fields        []string   `json:"conflict_fields,omitempty"`
	Unrecoverable int        `json:"unrecoverable_packets,omitempty"`
	Err           error      `json:"-"`
}

func (e *MediaError) Error() string {
	msg := fmt.Sprintf("%s/%s: %s", e.Stage, e.Code, e.Message)
	if e.TrackID != nil {
		msg += fmt.Sprintf(" track=%d", *e.TrackID)
	}
	if e.Timestamp != nil {
		msg += fmt.Sprintf(" pts_ms=%d", e.Timestamp.Millis())
	}
	if e.SourceOffset != nil {
		msg += fmt.Sprintf(" offset=%d", *e.SourceOffset)
	}
	if len(e.Fields) > 0 {
		msg += fmt.Sprintf(" fields=%v", e.Fields)
	}
	if e.Unrecoverable > 0 {
		msg += fmt.Sprintf(" unrecoverable=%d", e.Unrecoverable)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *MediaError) Unwrap() error { return e.Err }

func NewError(stage Stage, code ErrorCode, msg string, cause error) *MediaError {
	return &MediaError{Stage: stage, Code: code, Message: msg, Err: cause}
}

func AtOffset(err *MediaError, offset int64) *MediaError {
	err.SourceOffset = &offset
	return err
}

func AtTrack(err *MediaError, trackID uint32) *MediaError {
	err.TrackID = &trackID
	return err
}

func AtTimestamp(err *MediaError, ts Timestamp) *MediaError {
	cp := ts
	err.Timestamp = &cp
	return err
}

func AsMediaError(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

func HasCode(err error, code ErrorCode) bool {
	me, ok := AsMediaError(err)
	return ok && me.Code == code
}
