// Package clock provides the shared media time base used by every stage of
// the delivery pipeline. All public times are integer nanoseconds so that
// hashes and reports stay stable across machines.
package clock

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline stage that produced an error.
type Stage string

const (
	StageArgs       Stage = "args"
	StageParse      Stage = "parse"
	StageScan       Stage = "scan"
	StageRepair     Stage = "repair"
	StageIndex      Stage = "index"
	StageCheckpoint Stage = "checkpoint"
	StageVerify     Stage = "verify"
	StageEmit       Stage = "emit"
)

// Code is a stable, machine-readable error reason. It is part of the public
// demo contract and must not change silently.
type Code string

const (
	CodeBadArgs           Code = "bad_args"
	CodeBadManifest       Code = "bad_manifest"
	CodePathEscape        Code = "path_escape"
	CodeOversizedTable    Code = "oversized_table"
	CodeTruncatedTable    Code = "truncated_table"
	CodeBadTimescale      Code = "bad_timescale"
	CodeEditOverrun       Code = "edit_overrun"
	CodeDTSRegression     Code = "dts_regression"
	CodePTSOutOfRange     Code = "pts_out_of_range"
	CodeRTPWrap           Code = "rtp_wrap"
	CodeMissingSyncFlags  Code = "missing_sync_flags"
	CodeUnrepairableRange Code = "unrepairable_range"
	CodeCheckpointCorrupt Code = "checkpoint_corrupt"
	CodeCheckpointStale   Code = "checkpoint_stale"
	CodeVerification      Code = "verification_failed"
	CodeIndexIncomplete   Code = "index_incomplete"
	CodeIO                Code = "io_error"
)

// Error carries reviewable context: stage, track, original byte offset and a
// stable reason code. RawOffset refers to the source MP4 byte offset that a
// human can re-check with a hex dump.
type Error struct {
	Stage        Stage  `json:"stage"`
	Code         Code   `json:"error_code"`
	TrackID      uint32 `json:"track_id"`
	Sample       uint32 `json:"sample,omitempty"`
	RawOffset    int64  `json:"raw_offset"`
	Camera       string `json:"camera,omitempty"`
	ManifestLine int    `json:"manifest_line,omitempty"`
	Detail       string `json:"detail"`
	cause        error
}

// New creates a stage-tagged error with an unknown offset (-1).
func New(stage Stage, code Code, format string, args ...any) *Error {
	return &Error{Stage: stage, Code: code, RawOffset: -1, Detail: fmt.Sprintf(format, args...)}
}

func (e *Error) WithTrack(id uint32) *Error   { e.TrackID = id; return e }
func (e *Error) WithSample(n uint32) *Error   { e.Sample = n; return e }
func (e *Error) WithOffset(off int64) *Error  { e.RawOffset = off; return e }
func (e *Error) WithCamera(cam string) *Error { e.Camera = cam; return e }
func (e *Error) WithLine(line int) *Error     { e.ManifestLine = line; return e }

func (e *Error) WithCause(err error) *Error { e.cause = err; return e }

func (e *Error) Error() string {
	loc := ""
	if e.TrackID != 0 {
		loc += fmt.Sprintf(" track=%d", e.TrackID)
	}
	if e.Sample != 0 {
		loc += fmt.Sprintf(" sample=%d", e.Sample)
	}
	if e.RawOffset >= 0 {
		loc += fmt.Sprintf(" offset=%d", e.RawOffset)
	}
	msg := fmt.Sprintf("%s/%s:%s%s", e.Stage, e.Code, loc, " "+e.Detail)
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.cause }

// As finds a structured pipeline error.
func As(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// ExitCode maps failures to the demo contract: bad arguments or manifests
// exit 1, corrupt media and failed verification exit 2.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	pe, ok := As(err)
	if !ok {
		return 2
	}
	switch pe.Stage {
	case StageArgs, StageParse:
		return 1
	case StageCheckpoint:
		return 2
	default:
		return 2
	}
}
