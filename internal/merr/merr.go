// Package merr defines the unified error model shared by the EBML parser,
// the repair pipeline, the fan-out ladder and the fan-in merger.
//
// Every error carries a stable machine-readable code and, where meaningful,
// the byte offset in the source file at which the problem was detected. The
// CLI maps error codes to process exit codes: damaged input that still
// produced a recovery summary exits 2, everything else exits 1.
package merr

import (
	"errors"
	"fmt"
	"strings"
)

// Code is a stable machine-readable error code.
type Code string

const (
	// CodeTruncatedCluster marks input that ends in the middle of a cluster.
	CodeTruncatedCluster Code = "truncated_cluster"
	// CodeTruncatedElement marks input that ends inside a known-size element.
	CodeTruncatedElement Code = "truncated_element"
	// CodeElementTooLarge marks an element whose payload exceeds 8 MiB.
	CodeElementTooLarge Code = "element_too_large"
	// CodeInvalidVINT marks a malformed variable length integer.
	CodeInvalidVINT Code = "invalid_vint"
	// CodeUnknownSizeNotAllowed marks an unknown-size element that cannot be
	// streamed (only Segment, Cluster and BlockGroup may omit their size).
	CodeUnknownSizeNotAllowed Code = "unknown_size_not_allowed"
	// CodeInvalidBlock marks a SimpleBlock/Block payload that cannot be parsed.
	CodeInvalidBlock Code = "invalid_block"
	// CodeNoVideoKeyframes marks an index rebuild without keyframes.
	CodeNoVideoKeyframes Code = "no_video_keyframes"
	// CodeInvalidIndex marks an index that failed its own invariants.
	CodeInvalidIndex Code = "invalid_index"
	// CodeMP4CrossCheck marks a failed go-mp4 cross-check of the index.
	CodeMP4CrossCheck Code = "mp4_crosscheck_failed"
	// CodeLayerCanceled marks a fan-out layer canceled by its consumer.
	CodeLayerCanceled Code = "layer_canceled"
	// CodeBackpressureTimeout marks a publisher that could not make progress.
	CodeBackpressureTimeout Code = "backpressure_timeout"
	// CodeBoundaryMismatch marks a rejected representation switch.
	CodeBoundaryMismatch Code = "boundary_mismatch"
	// CodeBadInput marks invalid CLI input or fixtures.
	CodeBadInput Code = "bad_input"
	// CodeCanceled marks a canceled operation.
	CodeCanceled Code = "canceled"
)

// damageCodes are input defects that still allow a recovery summary. They map
// to exit code 2.
var damageCodes = map[Code]bool{
	CodeTruncatedCluster:      true,
	CodeTruncatedElement:      true,
	CodeElementTooLarge:       true,
	CodeInvalidVINT:           true,
	CodeUnknownSizeNotAllowed: true,
	CodeInvalidBlock:          true,
	CodeNoVideoKeyframes:      true,
	CodeInvalidIndex:          true,
}

// IsDamage reports whether code describes damaged input that can still be
// summarized.
func IsDamage(code Code) bool { return damageCodes[code] }

// Error is the unified error type.
type Error struct {
	Code   Code
	Offset int64 // byte offset in the source file, -1 when unknown
	Op     string
	Msg    string
	Err    error
}

// Error implements the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Op)
	b.WriteString(": ")
	b.WriteString(string(e.Code))
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " at byte %d", e.Offset)
	}
	if e.Msg != "" {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

// New builds an Error with a formatted message. Use offset -1 when the
// position is unknown.
func New(op string, code Code, offset int64, format string, args ...any) *Error {
	msg := fmt.Sprintf(format, args...)
	return &Error{Code: code, Offset: offset, Op: op, Msg: msg}
}

// Wrap attaches code/offset context to err, preserving the cause.
func Wrap(err error, op string, code Code, offset int64) *Error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Offset: offset, Op: op, Err: err}
}

// AsCode finds the first *Error with the requested code.
func AsCode(err error, code Code) (*Error, bool) {
	var target *Error
	for err != nil {
		if errors.As(err, &target) && target.Code == code {
			return target, true
		}
		err = errors.Unwrap(err)
	}
	return nil, false
}

// CodeOf returns the code of the outermost *Error, or CodeBadInput.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return CodeBadInput
}

// OffsetOf returns the byte offset of the first *Error, or -1.
func OffsetOf(err error) int64 {
	var target *Error
	if errors.As(err, &target) {
		return target.Offset
	}
	return -1
}

// ExitCode maps err to the documented process exit codes: 0 for nil, 2 for
// damaged input that was summarized, 1 otherwise.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if IsDamage(CodeOf(err)) {
		return 2
	}
	return 1
}
