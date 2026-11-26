package media

import (
	"errors"
	"fmt"
)

// ErrorCode enumerates the structured error kinds shared by every stage.
type ErrorCode string

const (
	// CodeInvalidTime: an unparseable or impossible time value. Such inputs
	// are never silently folded into the media time line.
	CodeInvalidTime ErrorCode = "invalid_time"
	// CodeInvalidInput: structurally invalid input (bad framing, zero
	// timescale, oversize file, ...).
	CodeInvalidInput ErrorCode = "invalid_input"
	// CodeCorruptBox: an MP4 box whose declared length field is impossible.
	CodeCorruptBox ErrorCode = "corrupt_box"
	// CodeTruncated: input ended before a declared structure was complete.
	CodeTruncated ErrorCode = "truncated"
	// CodeOversize: input exceeds the configured byte budget.
	CodeOversize ErrorCode = "oversize"
)

// Error is the single structured error model shared by all subsystems.
type Error struct {
	Code   ErrorCode
	Source string // asset / stream identifier
	Detail string
	// Offset is set for MP4 corruption: exact byte offset of the bad field.
	Offset int64
}

// Error implements error.
func (e *Error) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s: %s at byte %d: %s", e.Source, e.Code, e.Offset, e.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", e.Source, e.Code, e.Detail)
}

// AsError extracts a *media.Error from err, if present.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// InvalidTime builds a structured invalid-time error.
func InvalidTime(raw, detail string) *Error {
	return &Error{Code: CodeInvalidTime, Source: "time", Detail: detail + " (input=" + raw + ")", Offset: -1}
}

// InvalidInput builds a structured invalid-input error.
func InvalidInput(format string, args ...any) *Error {
	return &Error{Code: CodeInvalidInput, Source: "input", Detail: fmt.Sprintf(format, args...), Offset: -1}
}

// CorruptBox builds a structured MP4 corruption error. offset is the exact
// byte position of the offending size field inside the file.
func CorruptBox(source string, offset int64, detail string) *Error {
	return &Error{Code: CodeCorruptBox, Source: source, Offset: offset, Detail: detail}
}

// Truncated builds a structured truncation error.
func Truncated(source, detail string) *Error {
	return &Error{Code: CodeTruncated, Source: source, Detail: detail, Offset: -1}
}

// MaxInputBytes is the default per-input byte budget (64 MiB).
const MaxInputBytes int64 = 64 << 20
