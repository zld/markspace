// Package clock defines the single integer-nanosecond time base and the
// project-wide structured error model shared by parsing, alignment and
// recovery subsystems.
package clock

import "fmt"

// Tick is a point on the project-wide timeline, measured in integer
// nanoseconds. Every subsystem converts external time bases (Matroska
// timecode scale, RTP clock rates) into Tick values before comparing them,
// so alignment math never mixes units.
type Tick int64

// Nanoseconds returns the tick value as an integer nanosecond count.
func (t Tick) Nanoseconds() int64 { return int64(t) }

// Code is a stable, machine-readable error code. Numeric values are part of
// the public CLI contract and must never be renumbered.
type Code string

const (
	// CodeOK means no rule was violated.
	CodeOK Code = "OK"
	// CodeIllegalVINT means an EBML variable-length integer could not be
	// decoded (reserved all-ones width marker or truncated input).
	CodeIllegalVINT Code = "EBML_ILLEGAL_VINT"
	// CodeTruncatedElement means an element header or payload runs past
	// either its parent boundary or the end of file.
	CodeTruncatedElement Code = "EBML_TRUNCATED"
	// CodeBoundary means an unknown-length element appears where no legal
	// boundary exists (not the last child of its parent, or not at root).
	CodeBoundary Code = "EBML_BOUNDARY"
	// CodeOutOfRange means a numeric field lies outside its legal range.
	CodeOutOfRange Code = "EBML_OUT_OF_RANGE"
	// CodeTimeRegression means a block timecode moves backwards relative to
	// the previous block on the same track.
	CodeTimeRegression Code = "TIME_REGRESSION"
	// CodeTimeDuplicate means two blocks on the same track carry the same
	// absolute timecode.
	CodeTimeDuplicate Code = "TIME_DUPLICATE"
	// CodeAlignmentSkew means representation-layer skew inside a segment
	// exceeds the configured business threshold.
	CodeAlignmentSkew Code = "ALIGN_SKEW"
	// CodeRTPLoss means one or more RTP sequence numbers are missing.
	CodeRTPLoss Code = "RTP_LOSS"
	// CodeCacheMismatch means a cache entry failed source identity, source
	// range or content digest verification.
	CodeCacheMismatch Code = "CACHE_MISMATCH"
	// CodeCheckpoint means a checkpoint record could not be loaded or
	// failed integrity verification.
	CodeCheckpoint Code = "CHECKPOINT_INVALID"
	// CodeCancelled means context cancellation released a blocked worker.
	CodeCancelled Code = "CANCELLED"
	// CodeArgs means command-line arguments are invalid.
	CodeArgs Code = "ARGS_INVALID"
)

// TraceError carries an error code together with every locator required to
// find the problem in the source file: byte offset, element path and the
// offending raw value. All parse-time failures in the project use this type
// so the CLI can print offset/path/code uniformly.
type TraceError struct {
	Code   Code
	Offset int64
	Path   string
	Value  any
	Msg    string
}

// Error implements the error interface.
func (e *TraceError) Error() string {
	loc := e.Path
	if loc == "" {
		loc = "/"
	}
	return fmt.Sprintf("%s at offset=%d path=%s: %s", e.Code, e.Offset, loc, e.Msg)
}

// AtOffset returns a copy of the error annotated with the given offset.
func (e *TraceError) AtOffset(off int64) *TraceError {
	cp := *e
	cp.Offset = off
	return &cp
}

// AtPath returns a copy of the error annotated with the given element path.
func (e *TraceError) AtPath(path string) *TraceError {
	cp := *e
	cp.Path = path
	return &cp
}

// NewError builds a TraceError from code, offset, path and a message.
func NewError(code Code, off int64, path, msg string) *TraceError {
	return &TraceError{Code: code, Offset: off, Path: path, Msg: msg}
}

// AsTraceError extracts a *TraceError from err, or nil if err is none.
func AsTraceError(err error) *TraceError {
	if err == nil {
		return nil
	}
	if te, ok := err.(*TraceError); ok {
		return te
	}
	return nil
}

// Abs returns the absolute distance between two ticks as a positive Tick.
func Abs(a, b Tick) Tick {
	if a > b {
		return a - b
	}
	return b - a
}

// Max returns the larger of two ticks.
func Max(a, b Tick) Tick {
	if a > b {
		return a
	}
	return b
}

// Min returns the smaller of two ticks.
func Min(a, b Tick) Tick {
	if a < b {
		return a
	}
	return b
}
