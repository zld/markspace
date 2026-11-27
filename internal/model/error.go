package model

import "fmt"

// Stable error codes. They never depend on message wording so callers can
// branch on them and tests can assert against them.
const (
	ErrIndexOutOfRange     = "INDEX_OUT_OF_RANGE"
	ErrIndexOrder          = "INDEX_ORDER_VIOLATION"
	ErrIndexLength         = "INDEX_LENGTH_INVALID"
	ErrTruncatedUnit       = "ACCESS_UNIT_TRUNCATED"
	ErrIllegalHeader       = "ILLEGAL_CODEC_HEADER"
	ErrUnknownCodec        = "UNKNOWN_CODEC"
	ErrZeroTimebase        = "ZERO_TIMEBASE"
	ErrUnrepresentableTime = "UNREPRESENTABLE_TIME"
	ErrDigestMismatch      = "DIGEST_MISMATCH"
	ErrStageState          = "STAGE_STATE_INVALID"
	ErrUncommittedCleanup  = "UNCOMMITTED_ARTIFACT"
	ErrBufferLimit         = "BUFFER_LIMIT_EXCEEDED"
	ErrDuplicateManifest   = "DUPLICATE_MANIFEST_ENTRY"
)

// ExitCode maps structured errors to the documented process exit codes.
const (
	ExitOK       = 0
	ExitGeneric  = 1
	ExitCorrupt  = 2 // any malformed input artifact
	ExitCanceled = 130
)

// Error is the single structured error type used by every subsystem.
// ByteOffset is -1 when no stream position applies.
type Error struct {
	Code       string `json:"code"`
	Codec      string `json:"codec,omitempty"`
	ByteOffset int64  `json:"byte_offset"`
	Message    string `json:"message"`
	Cause      error  `json:"-"`
}

// New builds a structured error; offset is -1 when unknown.
func New(code, codec string, offset int64, format string, args ...any) *Error {
	return &Error{
		Code:       code,
		Codec:      codec,
		ByteOffset: offset,
		Message:    fmt.Sprintf(format, args...),
	}
}

// Wrap attaches a cause while preserving the structured fields.
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.Cause = cause
	return &cp
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Codec != "" && e.ByteOffset >= 0 {
		return fmt.Sprintf("%s [%s @ byte %d]: %s", e.Code, e.Codec, e.ByteOffset, e.Message)
	}
	if e.Codec != "" {
		return fmt.Sprintf("%s [%s]: %s", e.Code, e.Codec, e.Message)
	}
	if e.ByteOffset >= 0 {
		return fmt.Sprintf("%s [byte %d]: %s", e.Code, e.ByteOffset, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap supports errors.Is/errors.As on the wrapped cause.
func (e *Error) Unwrap() error { return e.Cause }

// ExitCode derives the process exit code associated with an error.
// Cancellation maps to 130, every other failure to 1 unless the caller has a
// more specific structured code; the demo layer promotes corrupt-input codes
// to 2 explicitly via IsCorrupt.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	if se, ok := AsStructured(err); ok && IsCorrupt(se.Code) {
		return ExitCorrupt
	}
	return ExitGeneric
}

// IsCorrupt reports whether the code means the input artifact itself is
// malformed (as opposed to an environment or internal failure).
func IsCorrupt(code string) bool {
	switch code {
	case ErrIndexOutOfRange, ErrIndexOrder, ErrIndexLength,
		ErrTruncatedUnit, ErrIllegalHeader, ErrUnknownCodec,
		ErrZeroTimebase, ErrDigestMismatch:
		return true
	}
	return false
}
