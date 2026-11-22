// Package timeline provides the unified nanosecond clock model, the
// timestamp repair logic and the stable error model shared by all
// subsystems. Every failure that crosses a module boundary is a *Fault
// carrying a stable class, the byte offset in the input and the track
// number (0 when unknown or not applicable).
package timeline

import (
	"errors"
	"fmt"
)

// Class is a stable, machine-readable error category. The string values
// are part of the tool's external contract (JSON summaries, exit codes)
// and must not be renamed.
type Class string

const (
	// ClassTruncated: input ends before a declared element boundary.
	ClassTruncated Class = "truncated"
	// ClassSizeOverflow: a declared element size is not representable
	// or exceeds the remaining input by an absurd margin.
	ClassSizeOverflow Class = "size_overflow"
	// ClassTimestampWrap: a large backward timestamp jump, repaired.
	ClassTimestampWrap Class = "timestamp_wrap"
	// ClassRegression: a small local timestamp regression, repaired.
	ClassRegression Class = "timestamp_regression"
	// ClassSyncExceeded: audio/video skew beyond the configured limit.
	ClassSyncExceeded Class = "sync_exceeded"
	// ClassTrackMapping: missing track, duplicate target, bad mapping.
	ClassTrackMapping Class = "track_mapping"
	// ClassIndexMismatch: index version or input digest mismatch.
	ClassIndexMismatch Class = "index_mismatch"
	// ClassCodec: incompatible or unsupported codec for the target.
	ClassCodec Class = "codec_incompatible"
	// ClassInterrupted: cancelled run; safe to resume, exit code 3.
	ClassInterrupted Class = "interrupted"
	// ClassInternal: any other failure, exit code 1.
	ClassInternal Class = "internal"
)

// Fault is the single error type crossing module boundaries.
type Fault struct {
	Class  Class  `json:"class"`
	Offset int64  `json:"offset"`
	Track  uint64 `json:"track"`
	Msg    string `json:"msg"`
}

func (f *Fault) Error() string {
	return fmt.Sprintf("%s at offset %d (track %d): %s", f.Class, f.Offset, f.Track, f.Msg)
}

// NewFault builds a Fault. track is 0 when unknown.
func NewFault(class Class, offset int64, track uint64, format string, args ...any) *Fault {
	return &Fault{Class: class, Offset: offset, Track: track, Msg: fmt.Sprintf(format, args...)}
}

// AsFault extracts a *Fault from err, wrapping non-fault errors as
// ClassInternal at offset -1.
func AsFault(err error) *Fault {
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	if err == nil {
		return nil
	}
	return &Fault{Class: ClassInternal, Offset: -1, Msg: err.Error()}
}

// ExitCode maps a fault class to the process exit status contract:
// 2 = corrupt input or mapping error, 3 = recoverable interrupt,
// 1 = internal error.
func (c Class) ExitCode() int {
	switch c {
	case ClassTruncated, ClassSizeOverflow, ClassSyncExceeded,
		ClassTrackMapping, ClassCodec:
		return 2
	case ClassInterrupted:
		return 3
	default:
		return 1
	}
}
