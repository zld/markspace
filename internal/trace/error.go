// Package trace defines the single error location model used by every
// mediacore subsystem.
package trace

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error category.
type Code string

const (
	CodeInvalidArgument      Code = "INVALID_ARGUMENT"
	CodeBoxLengthOutOfBounds Code = "BOX_LENGTH_OUT_OF_BOUNDS"
	CodeTruncated            Code = "INPUT_TRUNCATED"
	CodeMissingIndex         Code = "MISSING_INDEX"
	CodeTimestampRegression  Code = "TIMESTAMP_REGRESSION"
	CodeEmptyTrack           Code = "EMPTY_TRACK"
	CodeCutPointCorrupt      Code = "CUT_POINT_IN_CORRUPT_RANGE"
	CodePayloadMismatch      Code = "PAYLOAD_DIGEST_MISMATCH"
	CodeUnsupported          Code = "UNSUPPORTED"
	CodeInternal             Code = "INTERNAL"
)

// Stage identifies a processing stage.
type Stage string

const (
	StageIngest      Stage = "ingest.mp4"
	StageRTP         Stage = "ingest.rtp"
	StageRecover     Stage = "recover.index"
	StageNormalize   Stage = "model.timeline"
	StageCut         Stage = "segment.cut"
	StageAudio       Stage = "audio.silence"
	StageFingerprint Stage = "fingerprint.hash"
	StageSource      Stage = "store.source"
	StageDerived     Stage = "store.derived"
	StageSelfCheck   Stage = "cmd.selfcheck"
)

// Error is the unified error type. TrackID is -1 when no track applies.
// TimeNS and Offset are pointers-less flags so the struct remains comparable
// and JSON-friendly.
type Error struct {
	Code          Code    `json:"code"`
	Stage         Stage   `json:"stage"`
	Origin        Stage   `json:"origin_stage,omitempty"`
	Path          []Stage `json:"stage_path,omitempty"`
	Message       string  `json:"message"`
	TrackID       int     `json:"track_id"`
	HasTrack      bool    `json:"has_track"`
	TimeNS        int64   `json:"time_ns"`
	HasTime       bool    `json:"has_time"`
	ByteOffset    int64   `json:"byte_offset"`
	HasByteOffset bool    `json:"has_byte_offset"`
	Underlying    error   `json:"-"`
}

// E creates a located error.
func E(code Code, stage Stage, format string, args ...any) *Error {
	return &Error{
		Code:    code,
		Stage:   stage,
		Origin:  stage,
		Path:    []Stage{stage},
		Message: fmt.Sprintf(format, args...),
		TrackID: -1,
	}
}

// WithTrack attaches a track number.
func (e *Error) WithTrack(trackID int) *Error {
	cp := *e
	cp.TrackID = trackID
	cp.HasTrack = true
	return &cp
}

// WithTime attaches a unified nanosecond position.
func (e *Error) WithTime(timeNS int64) *Error {
	cp := *e
	cp.TimeNS = timeNS
	cp.HasTime = true
	return &cp
}

// WithOffset attaches an absolute byte offset.
func (e *Error) WithOffset(offset int64) *Error {
	cp := *e
	cp.ByteOffset = offset
	cp.HasByteOffset = true
	return &cp
}

// Wrap attaches the current stage while preserving the original category and
// location. A non-trace error becomes an INTERNAL trace error.
func Wrap(stage Stage, err error) error {
	if err == nil {
		return nil
	}
	var te *Error
	if !errors.As(err, &te) {
		return E(CodeInternal, stage, "%v", err).withUnderlying(err)
	}
	cp := *te
	cp.Underlying = err
	if cp.Origin == "" {
		cp.Origin = te.Stage
	}
	path := append([]Stage{}, te.Path...)
	if len(path) == 0 {
		path = []Stage{te.Stage}
	}
	if path[len(path)-1] != stage {
		path = append(path, stage)
	}
	cp.Path = path
	cp.Stage = stage
	return &cp
}

func (e *Error) withUnderlying(err error) *Error {
	e.Underlying = err
	return e
}

// Unwrap exposes wrapped errors.
func (e *Error) Unwrap() error { return e.Underlying }

// Error implements error.
func (e *Error) Error() string {
	parts := []string{string(e.Code), string(e.Stage), e.Message}
	if e.HasTrack {
		parts = append(parts, fmt.Sprintf("track=%d", e.TrackID))
	}
	if e.HasTime {
		parts = append(parts, fmt.Sprintf("time_ns=%d", e.TimeNS))
	}
	if e.HasByteOffset {
		parts = append(parts, fmt.Sprintf("byte_offset=%d", e.ByteOffset))
	}
	if e.Underlying != nil {
		parts = append(parts, e.Underlying.Error())
	}
	return joinParts(parts)
}

func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ": "
		}
		out += p
	}
	return out
}

// As finds the first *Error in err's chain.
func As(err error) (*Error, bool) {
	var te *Error
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

// HasCode reports whether any error in err's chain has code.
func HasCode(err error, code Code) bool {
	for err != nil {
		var te *Error
		if errors.As(err, &te) && te.Code == code {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}
