// Package cerr defines the stable error model shared by the continuity
// pipeline subsystems. Every failure carries a stable machine-readable
// code, the byte offset of the offending input (-1 when not applicable)
// and the element or subsystem path that located the failure.
package cerr

import (
	"errors"
	"fmt"
)

// Stable error codes. These strings are part of the tool's contract and
// must not change between runs.
const (
	CodeVINTInvalid       = "EBML_VINT_INVALID"
	CodeSizeOverflow      = "EBML_SIZE_OVERFLOW"
	CodeTruncatedHeader   = "EBML_TRUNCATED_HEADER"
	CodeTruncatedData     = "EBML_TRUNCATED_DATA"
	CodeUnknownSizeBounds = "EBML_UNKNOWN_SIZE_BOUNDS"
	CodeTimestampOverflow = "TS_OVERFLOW"
	CodeLayoutUnsupported = "TRACK_LAYOUT_UNSUPPORTED"
	CodeRateUnsupported   = "TRACK_RATE_UNSUPPORTED"
	CodeTypeUnsupported   = "TRACK_TYPE_UNSUPPORTED"
	CodeMetadataConflict  = "TRACK_METADATA_CONFLICT"
	CodeLacingUnsupported = "BLOCK_LACING_UNSUPPORTED"
	CodeWorkerFailed      = "POOL_WORKER_FAILED"
	CodeInvalidArgument   = "ARG_INVALID"
)

// Error is the shared structured error. Offset is the absolute byte
// offset into the input, or -1 when the failure is not tied to a byte
// position. Path is an EBML element path such as
// "/Segment/Cluster/SimpleBlock" or a subsystem path such as "track[2]".
type Error struct {
	Code   string
	Offset int64
	Path   string
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s offset=%d path=%s: %s", e.Code, e.Offset, e.Path, e.Msg)
}

// New builds an *Error.
func New(code string, offset int64, path, msg string) *Error {
	return &Error{Code: code, Offset: offset, Path: path, Msg: msg}
}

// Is reports whether err wraps an *Error carrying the given stable code.
func Is(err error, code string) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == code
	}
	return false
}

// Locate returns code, offset and path of err if it wraps an *Error,
// otherwise a generic internal-error triple.
func Locate(err error) (code string, offset int64, path string) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, e.Offset, e.Path
	}
	return "INTERNAL", -1, ""
}
