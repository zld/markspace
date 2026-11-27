// Package clock defines shared error codes and the unified timebase mapping
// used across the media kernel.
package clock

import "fmt"

// Code is a stable, machine-readable error code. Codes are part of the
// delivered contract: corrupt-input diagnostics print them verbatim.
type Code string

const (
	// EBML structural errors.
	ErrInvalidVINT       Code = "ERR_INVALID_VINT"
	ErrTruncatedHeader   Code = "ERR_TRUNCATED_HEADER"
	ErrSizeOverflow      Code = "ERR_SIZE_OVERFLOW"
	ErrBounds            Code = "ERR_BOUNDS"
	ErrUnknownSizeMisuse Code = "ERR_UNKNOWN_SIZE_MISUSE"
	ErrCorruptBlock      Code = "ERR_CORRUPT_BLOCK"
	ErrUnsupported       Code = "ERR_UNSUPPORTED"

	// Timebase mapping errors.
	ErrZeroScale       Code = "ERR_ZERO_SCALE"
	ErrOverflow        Code = "ERR_OVERFLOW"
	ErrUnmappableTrack Code = "ERR_UNMAPPABLE_TRACK"

	// Job / merge errors.
	ErrDuplicateKey   Code = "ERR_DUPLICATE_KEY"
	ErrKeyConflict    Code = "ERR_KEY_CONFLICT"
	ErrCheckpoint     Code = "ERR_CHECKPOINT"
	ErrNoFeasiblePlan Code = "ERR_NO_FEASIBLE_PLAN"
)

// Error carries a stable code plus optional file-localization fields.
// Offset is -1 when not applicable; Path is the EBML element path when known.
type Error struct {
	Code   Code
	Offset int64
	Path   string
	Msg    string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("code=%s", e.Code)
	if e.Offset >= 0 {
		s += fmt.Sprintf(" byte_offset=%d", e.Offset)
	}
	if e.Path != "" {
		s += fmt.Sprintf(" element_path=%s", e.Path)
	}
	if e.Msg != "" {
		s += " msg=" + e.Msg
	}
	return s
}

// New builds an *Error with no localization.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Offset: -1, Msg: fmt.Sprintf(format, args...)}
}

// At builds an *Error localized to a byte offset and element path.
func At(code Code, offset int64, path, format string, args ...any) *Error {
	return &Error{Code: code, Offset: offset, Path: path, Msg: fmt.Sprintf(format, args...)}
}

// CodeOf extracts the stable code from an error, or "" if none.
func CodeOf(err error) Code {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}
