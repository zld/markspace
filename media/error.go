package media

import "fmt"

// Code is a stable, machine-readable error classification. Codes are part
// of the kernel's contract: callers switch on Code, never on message text.
type Code string

const (
	// Input faults (exit class 2: corrupt input, quarantined).
	CodeIndexInvalid      Code = "index_invalid"       // keyframe index out of bounds / overlapping / digest mismatch
	CodeNALCorrupt        Code = "nal_corrupt"         // malformed NAL unit in raw bitstream
	CodeLayoutUnknown     Code = "layout_unknown"      // undeclared channel layout, no silent remix
	CodeColorConflict     Code = "color_conflict"      // conflicting color metadata
	CodeCacheSource       Code = "cache_source_mismatch" // cache hit with wrong source identity
	CodeCacheDigest       Code = "cache_digest_mismatch" // cache hit with wrong content digest
	CodeCheckpointCorrupt Code = "checkpoint_corrupt"  // checkpoint failed integrity check
	CodeLimitExceeded     Code = "limit_exceeded"      // index/packet length beyond hard bound

	// Internal faults (exit class 1).
	CodeInternal Code = "internal"
)

// Class groups codes into the process exit model.
type Class int

const (
	ClassInput    Class = iota // corrupt/untrusted input: quarantine, exit 2
	ClassInternal              // kernel bug or resource failure: exit 1
)

// ClassOf maps a code to its exit class.
func ClassOf(c Code) Class {
	switch c {
	case CodeInternal:
		return ClassInternal
	default:
		return ClassInput
	}
}

// Error is the kernel's single error type. Every subsystem returns *Error
// (possibly wrapping a cause); Offset locates the fault in the input when
// meaningful (-1 when not applicable).
type Error struct {
	Code   Code
	Op     string // subsystem.operation, e.g. "ingest.ValidateIndex"
	Offset int64  // byte/sample offset of the fault, -1 if not locatable
	Detail string
	Err    error // wrapped cause
}

func (e *Error) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s: %s at offset %d: %s", e.Op, e.Code, e.Offset, e.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", e.Op, e.Code, e.Detail)
}

// Unwrap returns the wrapped cause, if any.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error with the same Code, so
// errors.Is(err, &media.Error{Code: media.CodeIndexInvalid}) classifies
// without string matching.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Fault builds an *Error with no wrapped cause.
func Fault(code Code, op string, offset int64, format string, args ...any) *Error {
	return &Error{Code: code, Op: op, Offset: offset, Detail: fmt.Sprintf(format, args...)}
}

// Wrap builds an *Error wrapping a lower-level cause.
func Wrap(code Code, op string, offset int64, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Op: op, Offset: offset, Detail: fmt.Sprintf(format, args...), Err: cause}
}

// CodeOf extracts the stable code from any error, defaulting to
// CodeInternal for foreign errors.
func CodeOf(err error) Code {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return CodeInternal
}
