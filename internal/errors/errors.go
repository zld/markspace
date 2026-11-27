// Package errors defines the shared, stable error model for the media kernel.
//
// Every recoverable failure produced by a subsystem is represented by a
// [MediaError] carrying a stable [Code] and, when the failure originates from
// a container file, the byte offset at which the problem was detected.
package errors

import "fmt"

// Code is a stable, machine-readable error identifier. Numeric values are part
// of the contract and must never be reused with a different meaning.
type Code int

const (
	CodeOK Code = 0

	// Container / file level failures (1xx).
	CodeTruncated      Code = 100 // a box or sample runs past the end of file
	CodeBoxOutOfRange  Code = 101 // a declared box length is outside the file
	CodeInvalidBox     Code = 102 // a box cannot be parsed
	CodeUnsupported    Code = 103 // container feature is not supported
	CodeSampleMissing  Code = 104 // sample table references absent data

	// Timeline failures (2xx).
	CodeWrapBroken     Code = 200 // counter wrapped without provable continuity
	CodeTimeWentBack   Code = 201 // a timestamp would move the timeline backwards
	CodeDriftTooLarge  Code = 202 // clock drift exceeds the safety bound
	CodeDiscontinuity  Code = 203 // gap that cannot be explained by wrap/loss

	// RTP recovery failures (3xx).
	CodeLateOutsideWindow Code = 300 // late packet arrived after window slid
	CodeSSRCMismatch      Code = 301 // stream changed SSRC unexpectedly

	// Audio failures (4xx).
	CodeUndecodable     Code = 400 // sample payload cannot be decoded
	CodeInsufficient    Code = 401 // not enough data to fill a frame
	CodeClipping        Code = 402 // output sample was clipped
	CodeLayoutMismatch  Code = 403 // channel layout unsupported

	// Checkpoint / index failures (5xx).
	CodeCheckpointCorrupt Code = 500 // checkpoint file unreadable / invalid
	CodeIndexMismatch     Code = 501 // index record does not match output
	CodeChunkIncomplete   Code = 502 // checkpoint points at partial data
)

// ExitCode maps an error Code to the process exit code contract:
//
//	0 - success
//	2 - corrupted input
//	3 - checkpoint or index inconsistency
//	1 - any other failure
func (c Code) ExitCode() int {
	switch {
	case c == CodeOK:
		return 0
	case c >= 100 && c < 200:
		return 2
	case c >= 400 && c < 500:
		return 2
	case c >= 500:
		return 3
	default:
		return 1
	}
}

// String returns the stable identifier name used in JSON output.
func (c Code) String() string {
	switch c {
	case CodeOK:
		return "ok"
	case CodeTruncated:
		return "truncated"
	case CodeBoxOutOfRange:
		return "box_out_of_range"
	case CodeInvalidBox:
		return "invalid_box"
	case CodeUnsupported:
		return "unsupported"
	case CodeSampleMissing:
		return "sample_missing"
	case CodeWrapBroken:
		return "wrap_broken"
	case CodeTimeWentBack:
		return "time_went_back"
	case CodeDriftTooLarge:
		return "drift_too_large"
	case CodeDiscontinuity:
		return "discontinuity"
	case CodeLateOutsideWindow:
		return "late_outside_window"
	case CodeSSRCMismatch:
		return "ssrc_mismatch"
	case CodeUndecodable:
		return "undecodable"
	case CodeInsufficient:
		return "insufficient_data"
	case CodeClipping:
		return "clipping"
	case CodeLayoutMismatch:
		return "layout_mismatch"
	case CodeCheckpointCorrupt:
		return "checkpoint_corrupt"
	case CodeIndexMismatch:
		return "index_mismatch"
	case CodeChunkIncomplete:
		return "chunk_incomplete"
	default:
		return fmt.Sprintf("code_%d", int(c))
	}
}

// MediaError is the shared error type carrying a stable code and location.
type MediaError struct {
	Code   Code   `json:"code"`
	Offset int64  `json:"offset,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// New builds a MediaError without a file offset.
func New(code Code, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Offset: -1, Detail: fmt.Sprintf(format, args...)}
}

// At builds a MediaError anchored at a byte offset inside the input file.
func At(code Code, offset int64, format string, args ...any) *MediaError {
	return &MediaError{Code: code, Offset: offset, Detail: fmt.Sprintf(format, args...)}
}

func (e *MediaError) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s at offset %d: %s", e.Code, e.Offset, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// WithOffset returns a copy of the error annotated with a file offset.
func (e *MediaError) WithOffset(offset int64) *MediaError {
	cp := *e
	cp.Offset = offset
	return &cp
}

// AsCode extracts the stable Code from any error, defaulting to a generic
// non-zero code for errors outside this model.
func AsCode(err error) Code {
	if err == nil {
		return CodeOK
	}
	var me *MediaError
	if As(err, &me) {
		return me.Code
	}
	return CodeInvalidBox
}

// As extracts the first *MediaError in err's tree into target.
func As(err error, target **MediaError) bool {
	for err != nil {
		if me, ok := err.(*MediaError); ok {
			*target = me
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
