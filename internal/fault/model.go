// Package fault defines stable, locatable error classifications used across
// scanning, recovery, remuxing, and verification.
package fault

import (
	"errors"
	"fmt"
)

// Code is a machine-readable failure class.
type Code string

const (
	InvalidArgument   Code = "invalid_argument"
	IOFailure         Code = "io_failure"
	NotFound          Code = "not_found"
	DigestMismatch    Code = "digest_mismatch"
	UnsupportedFormat Code = "unsupported_format"
	UnsupportedCodec  Code = "unsupported_codec"
	CorruptHeader     Code = "corrupt_header"
	TruncatedData     Code = "truncated_data"
	IndexExpired      Code = "index_expired"
	GapDetected       Code = "gap_detected"
	LimitExceeded     Code = "limit_exceeded"
	Verification      Code = "verification_failed"
	Conflict          Code = "conflict"
)

// Error carries a class and optional file/container offset.
type Error struct {
	Code   Code   `json:"code"`
	Path   string `json:"path,omitempty"`
	Offset int64  `json:"offset"`
	Reason string `json:"reason,omitempty"`
	Err    error  `json:"-"`
}

func (e *Error) Error() string {
	where := ""
	if e.Path != "" {
		where = e.Path
	}
	if e.Offset >= 0 {
		if where != "" {
			where += fmt.Sprintf(":@%d", e.Offset)
		} else {
			where = fmt.Sprintf("offset %d", e.Offset)
		}
	}
	msg := string(e.Code)
	if where != "" {
		msg += " at " + where
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// New creates a classified error without a cause.
func New(code Code, reason string) *Error {
	return &Error{Code: code, Offset: -1, Reason: reason}
}

// Wrap classifies an existing error.
func Wrap(code Code, err error) *Error {
	return &Error{Code: code, Offset: -1, Err: err}
}

// At adds path and offset context. Negative offsets mean unknown.
func At(err error, path string, offset int64) error {
	var fe *Error
	if errors.As(err, &fe) {
		cp := *fe
		if path != "" && cp.Path == "" {
			cp.Path = path
		}
		if offset >= 0 && cp.Offset < 0 {
			cp.Offset = offset
		}
		return &cp
	}
	return &Error{Code: IOFailure, Path: path, Offset: offset, Err: err}
}

// Class returns the classified code, or "" for ordinary errors.
func Class(err error) Code {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return ""
}

// IsCode reports whether err contains code c.
func IsCode(err error, c Code) bool {
	var fe *Error
	return errors.As(err, &fe) && fe.Code == c
}
