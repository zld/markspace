// Package fault defines the structured, cross-module error model used by every
// subsystem of the media kernel. Every error that can be attributed to a
// concrete location in the input carries an absolute byte offset so that the
// batch runner can decide whether resuming from a checkpoint is safe.
package fault

import (
	"errors"
	"fmt"
)

// Kind enumerates the fault classes understood across modules.
type Kind string

const (
	// KindIllegalVINT: an EBML variable-length integer cannot be decoded
	// (no length marker, zero-width, or truncated at EOF).
	KindIllegalVINT Kind = "illegal_vint"
	// KindTruncated: the input ends before an element or block completes.
	KindTruncated Kind = "truncated"
	// KindLengthOutOfRange: a declared element length exceeds its parent,
	// the file size, or the maximum representable block range.
	KindLengthOutOfRange Kind = "length_out_of_range"
	// KindUnsupportedCodec: a track codec is not in the supported set.
	KindUnsupportedCodec Kind = "unsupported_codec"
	// KindTimestampRegression: a block timestamp is invalid or moves
	// backwards relative to the track's monotonic timeline.
	KindTimestampRegression Kind = "timestamp_regression"
	// KindCheckpointCorrupt: the local index cannot be parsed or fails
	// its checksum. Recovery is possible by discarding the index.
	KindCheckpointCorrupt Kind = "checkpoint_corrupt"
	// KindInputChanged: the input file no longer matches the checkpoint
	// (size or digest changed). The run must restart from scratch.
	KindInputChanged Kind = "input_changed"
	// KindRTPSequence: an RTP sequence discontinuity (duplicate or jump).
	KindRTPSequence Kind = "rtp_sequence"
	// KindPayloadMismatch: a per-block payload hash differs between the
	// input and the remuxed output.
	KindPayloadMismatch Kind = "payload_mismatch"
	// KindIO: an underlying operating-system level I/O failure.
	KindIO Kind = "io"
)

// Error is the structured error shared by all subsystems. It carries a fault
// Kind, an absolute input Offset (or -1 when the fault is not tied to a byte
// position), an optional Track number and a human-readable Message.
type Error struct {
	Kind    Kind
	Offset  int64
	Track   int
	Message string
	// Recoverable marks faults after which a run may resume from the last
	// committed checkpoint (e.g. truncated tail, corrupt index).
	Recoverable bool
}

// New builds a fault at an absolute byte offset.
func New(kind Kind, offset int64, format string, args ...any) *Error {
	return &Error{Kind: kind, Offset: offset, Message: fmt.Sprintf(format, args...)}
}

// NewTrack builds a fault attributed to a specific track.
func NewTrack(kind Kind, track int, offset int64, format string, args ...any) *Error {
	return &Error{Kind: kind, Offset: offset, Track: track, Message: fmt.Sprintf(format, args...)}
}

// AsRecoverable flags the fault as resumable from a prior checkpoint.
func (e *Error) AsRecoverable() *Error {
	e.Recoverable = true
	return e
}

// Error implements the error interface.
func (e *Error) Error() string {
	loc := "offset=-1"
	if e.Offset >= 0 {
		loc = fmt.Sprintf("offset=%d", e.Offset)
	}
	if e.Track > 0 {
		return fmt.Sprintf("%s: track=%d %s: %s", e.Kind, e.Track, loc, e.Message)
	}
	return fmt.Sprintf("%s: %s: %s", e.Kind, loc, e.Message)
}

// Is supports errors.Is comparisons on Kind in addition to pointer identity.
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return e.Kind == t.Kind
	}
	return false
}

// Of extracts a *Error from any error, returning nil when the error does not
// embed the structured fault model.
func Of(err error) *Error {
	var fe *Error
	if errors.As(err, &fe) {
		return fe
	}
	return nil
}

// Is reports whether err carries the given fault Kind.
func Is(err error, kind Kind) bool {
	if fe := Of(err); fe != nil {
		return fe.Kind == kind
	}
	return false
}

// IsRecoverable reports whether a batch run can resume after this error.
func IsRecoverable(err error) bool {
	if fe := Of(err); fe != nil {
		return fe.Recoverable
	}
	return false
}
