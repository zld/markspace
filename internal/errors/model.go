package errors

import (
	"fmt"

	"media-audit/internal/timebase"
)

type Code string

const (
	CodeUnknownStructure  Code = "UNKNOWN_STRUCTURE"
	CodeTruncated         Code = "TRUNCATED"
	CodeInvalidLength     Code = "INVALID_LENGTH"
	CodeChecksumMismatch  Code = "CHECKSUM_MISMATCH"
	CodeInvalidTimestamp  Code = "INVALID_TIMESTAMP"
	CodeTimestampWrap     Code = "TIMESTAMP_WRAP"
	CodeMetadataConflict  Code = "METADATA_CONFLICT"
	CodeFingerprintChange Code = "FINGERPRINT_CHANGE"
	CodeCheckpointCorrupt Code = "CHECKPOINT_CORRUPT"
)

// ByteRange is half-open [Start, End) when End > Start. A zero-length range
// at an EOF position uses Start == End == EOF and Offset carries that position.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func (r ByteRange) Len() int64 { return r.End - r.Start }

// Error is the traceable exception record shared by parsers, recovery and
// manifests. Offset is the first byte at which failure is observable.
type Error struct {
	Code      Code            `json:"code"`
	Message   string          `json:"message"`
	Path      string          `json:"path,omitempty"`
	TrackID   *uint32         `json:"track_id,omitempty"`
	Interval  *timebase.Range `json:"interval,omitempty"`
	Bytes     *ByteRange      `json:"byte_range,omitempty"`
	Offset    int64           `json:"offset"`
	Expected  string          `json:"expected,omitempty"`
	Actual    string          `json:"actual,omitempty"`
	Recovered bool            `json:"recovered,omitempty"`
}

func New(code Code, offset int64, format string, args ...any) *Error {
	return &Error{Code: code, Offset: offset, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) WithPath(path string) *Error {
	e.Path = path
	return e
}

func (e *Error) WithTrack(id uint32) *Error {
	v := id
	e.TrackID = &v
	return e
}

func (e *Error) WithBytes(start, end int64) *Error {
	e.Bytes = &ByteRange{Start: start, End: end}
	if e.Offset == 0 && start > 0 {
		e.Offset = start
	}
	return e
}

func (e *Error) WithInterval(r timebase.Range) *Error {
	e.Interval = &r
	return e
}

func (e *Error) WithExpectedActual(expected, actual string) *Error {
	e.Expected = expected
	e.Actual = actual
	return e
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s at byte %d: %s", e.Code, e.Offset, e.Message)
}
