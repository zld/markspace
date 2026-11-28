package mediakernel

import "fmt"

// Code is a stable machine-readable error code.
type Code string

const (
	ErrInvalidVINT      Code = "EBML_INVALID_VINT"
	ErrTruncatedHeader  Code = "EBML_TRUNCATED_HEADER"
	ErrSizeOverflow     Code = "EBML_SIZE_OVERFLOW"
	ErrParentBoundary   Code = "EBML_PARENT_BOUNDARY"
	ErrUnknownSizeLeaf  Code = "EBML_UNKNOWN_SIZE_LEAF"
	ErrBlockTooLarge    Code = "BLOCK_TOO_LARGE"
	ErrLacingUnsupported Code = "BLOCK_LACING_UNSUPPORTED"
	ErrTSMissing        Code = "TS_MISSING"
	ErrTSRegression          Code = "TS_REGRESSION"
	ErrTSOverflow       Code = "TS_OVERFLOW"
	ErrInputChanged     Code = "CHECKPOINT_INPUT_MISMATCH"
	ErrCanceled         Code = "PIPELINE_CANCELED"
)

// Error is a structured error carrying a stable code, the byte offset of the
// first offending byte and the element path where the failure occurred.
type Error struct {
	Code   Code   `json:"code"`
	Offset int64  `json:"offset"`
	Path   string `json:"path"`
	Msg    string `json:"msg"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s at offset %d [%s]: %s", e.Code, e.Offset, e.Path, e.Msg)
}

func codeOf(err error) (Code, bool) {
	if e, ok := err.(*Error); ok {
		return e.Code, true
	}
	return "", false
}
