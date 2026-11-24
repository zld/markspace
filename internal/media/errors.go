package media

import (
	"errors"
	"fmt"
)

// Stage identifies a pipeline phase.
type Stage string

const (
	StageParse     Stage = "parse"
	StageTransform Stage = "transform"
	StageIndex     Stage = "index"
	StageVerify    Stage = "verify"
)

// ErrorCode is a stable machine-readable failure category.
type ErrorCode string

const (
	CodeOK            ErrorCode = "OK"
	CodeInvalidConfig ErrorCode = "INVALID_CONFIG"
	CodeUnsupported   ErrorCode = "UNSUPPORTED_FORMAT"
	CodeBadRIFF       ErrorCode = "BAD_RIFF"
	CodeTruncated     ErrorCode = "WAV_TRUNCATED"
	CodeBlockTooLarge ErrorCode = "BLOCK_TOO_LARGE"
	CodeIndexCorrupt  ErrorCode = "INDEX_CORRUPT"
	CodeValidation    ErrorCode = "VALIDATION_FAILED"
	CodeInterrupted   ErrorCode = "INTERRUPTED"
	CodeInternal      ErrorCode = "INTERNAL_ERROR"
)

// Error carries the stage and original byte offset associated with a failure.
// Offset is -1 when no source offset is meaningful.
type Error struct {
	Code   ErrorCode `json:"error_code"`
	Stage  Stage     `json:"stage"`
	Offset int64     `json:"corrupt_offset"`
	Reason string    `json:"reason,omitempty"`
	Err    error     `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	msg := fmt.Sprintf("%s at stage=%s offset=%d: %s", e.Code, e.Stage, e.Offset, e.Reason)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(code ErrorCode, stage Stage, offset int64, reason string, err error) *Error {
	return &Error{Code: code, Stage: stage, Offset: offset, Reason: reason, Err: err}
}

// AsError finds an *Error in err's chain.
func AsError(err error) *Error {
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return nil
}

// ExitCode maps kernel errors to the contract's process exit codes.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	me := AsError(err)
	if me == nil {
		return 1
	}
	switch me.Code {
	case CodeInterrupted:
		return 3
	case CodeInvalidConfig, CodeUnsupported, CodeBadRIFF, CodeTruncated,
		CodeBlockTooLarge, CodeValidation:
		return 2
	default:
		return 1
	}
}
