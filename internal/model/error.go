// Package model defines the shared error taxonomy and the unified fragment
// event that every protocol adapter (HLS/DASH/MP4/RTP) emits.
package model

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline phase in which a problem was detected.
type Stage string

const (
	StageParse      Stage = "parse"
	StageBuffer     Stage = "buffer"
	StageRecovery   Stage = "recovery"
	StageSync       Stage = "sync"
	StageNormalize  Stage = "normalize"
	StagePackage    Stage = "package"
	StageCheckpoint Stage = "checkpoint"
)

// Code is the machine-readable, layered error classification.
type Code string

const (
	CodeConfig            Code = "config_invalid"
	CodeZeroTimescale     Code = "zero_timescale"
	CodeTimestampOverflow Code = "timestamp_overflow"
	CodeTimestampJump     Code = "timestamp_jump"
	CodeDuplicateSeq      Code = "duplicate_sequence"
	CodeOutOfWindow       Code = "out_of_window"
	CodeMissingReference  Code = "missing_reference"
	CodeTruncatedFragment Code = "truncated_fragment"
	CodeUnrecoverableLoss Code = "unrecoverable_loss"
	CodeSyncDrift         Code = "sync_drift"
	CodeChannelDrop       Code = "channel_layout_unsafe"
	CodeSampleRateClash   Code = "sample_rate_clash"
	CodeColorConflict     Code = "color_conflict"
	CodeUnknownColor      Code = "unknown_color"
	CodeInvariant         Code = "invariant_violation"
)

// Recoverable reports whether a stage further down the pipeline can repair the
// condition (e.g. FEC or bounded waiting) instead of failing the material.
func (c Code) Recoverable() bool {
	switch c {
	case CodeOutOfWindow, CodeTruncatedFragment, CodeUnrecoverableLoss,
		CodeInvariant, CodeZeroTimescale, CodeTimestampOverflow,
		CodeChannelDrop, CodeSyncDrift:
		return false
	default:
		return true
	}
}

// MediaError carries the full diagnostic coordinates demanded by the spec:
// material, track, stage and recoverability. It wraps an optional cause.
type MediaError struct {
	Code        Code
	Stage       Stage
	MaterialID  string
	TrackID     string
	Recoverable bool
	Message     string
	Cause       error
}

func (e *MediaError) Error() string {
	loc := e.MaterialID
	if e.TrackID != "" {
		loc += "/" + e.TrackID
	}
	rec := "unrecoverable"
	if e.Recoverable {
		rec = "recoverable"
	}
	return fmt.Sprintf("[%s|%s|%s|%s] %s", e.Code, e.Stage, loc, rec, e.Message)
}

func (e *MediaError) Unwrap() error { return e.Cause }

// NewError builds a MediaError, deriving recoverability from the code.
func NewError(code Code, stage Stage, materialID, trackID, msg string) *MediaError {
	return &MediaError{
		Code:        code,
		Stage:       stage,
		MaterialID:  materialID,
		TrackID:     trackID,
		Recoverable: code.Recoverable(),
		Message:     msg,
	}
}

// Wrap annotates an existing error with pipeline coordinates.
func Wrap(err error, code Code, stage Stage, materialID, trackID, msg string) *MediaError {
	return &MediaError{
		Code:        code,
		Stage:       stage,
		MaterialID:  materialID,
		TrackID:     trackID,
		Recoverable: code.Recoverable(),
		Message:     msg,
		Cause:       err,
	}
}

// HasCode reports whether err (or any wrapped error) carries the given code.
func HasCode(err error, code Code) bool {
	var me *MediaError
	if errors.As(err, &me) {
		return me.Code == code
	}
	return false
}

// IsRecoverable reports whether the first MediaError in the chain is marked
// recoverable. A nil error is trivially "fine" and returns true.
func IsRecoverable(err error) bool {
	if err == nil {
		return true
	}
	var me *MediaError
	if errors.As(err, &me) {
		return me.Recoverable
	}
	return false
}
