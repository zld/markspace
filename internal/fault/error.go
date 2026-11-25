// Package fault defines the unified error model shared by every media
// processing subsystem. Errors carry the input byte offset, the track number
// (when applicable) and whether recovery from a local checkpoint is possible.
package fault

import (
	"errors"
	"fmt"
)

// Category classifies an error for routing, metrics and exit-code mapping.
type Category string

const (
	CatHeader          Category = "header_mismatch"    // contradictory WAV/RTP header fields
	CatTruncated       Category = "truncated_input"    // declared chunk/frame data is missing
	CatAlignment       Category = "frame_alignment"    // non-integral sample frame tail
	CatRTPPacket       Category = "rtp_packet"         // malformed RTP packet
	CatRTPGap          Category = "rtp_gap"            // unresolvable sequence number gap
	CatMapping         Category = "mapping"            // ambiguous or invalid channel mapping
	CatUnsafePath      Category = "unsafe_path"        // path escape or symlink attempt
	CatCheckpoint      Category = "checkpoint_corrupt" // truncated or unparsable index
	CatHash            Category = "hash_mismatch"      // segment content hash mismatch
	CatWrite           Category = "write_failure"      // disk/MP4 write or parse failure
	CatCanceled        Category = "canceled"           // task canceled by context
)

const (
	noOffset = int64(-1)
	noTrack  = -1
)

// Error is the only structured error type returned across the pipeline.
type Error struct {
	Category    Category
	Op          string // optional operation name, e.g. "wav.feed"
	Offset      int64  // byte offset inside the input stream, -1 when unknown
	Track       int    // zero-based target track, -1 when not applicable
	Recoverable bool   // true when a checkpoint resume can redo the work
	Err         error  // wrapped cause, may be nil
}

// New builds an Error without positional context.
func New(cat Category, op string, recoverable bool, msg string) *Error {
	return &Error{
		Category:    cat,
		Op:          op,
		Offset:      noOffset,
		Track:       noTrack,
		Recoverable: recoverable,
		Err:         errors.New(msg),
	}
}

// Wrap annotates a cause with category and positional context.
func Wrap(cat Category, op string, offset int64, track int, recoverable bool, cause error) *Error {
	return &Error{
		Category:    cat,
		Op:          op,
		Offset:      offset,
		Track:       track,
		Recoverable: recoverable,
		Err:         cause,
	}
}

// WithOffset returns a shallow copy with the input offset populated.
func (e *Error) WithOffset(offset int64) *Error {
	cp := *e
	cp.Offset = offset
	return &cp
}

// WithTrack returns a shallow copy with the track number populated.
func (e *Error) WithTrack(track int) *Error {
	cp := *e
	cp.Track = track
	return &cp
}

func (e *Error) Error() string {
	s := string(e.Category)
	if e.Op != "" {
		s += " [" + e.Op + "]"
	}
	if e.Offset != noOffset {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Track != noTrack {
		s += fmt.Sprintf(" track=%d", e.Track)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Err }

// As extracts a *fault.Error from any wrapped error chain.
func As(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// ExitCode maps an error to the documented process exit codes:
// 0 success, 2 input/index corruption, 3 internal write or cancellation.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	fe, ok := As(err)
	if !ok {
		return 3
	}
	switch fe.Category {
	case CatHeader, CatTruncated, CatAlignment, CatRTPPacket,
		CatRTPGap, CatMapping, CatUnsafePath, CatCheckpoint, CatHash:
		return 2
	case CatWrite, CatCanceled:
		return 3
	default:
		return 3
	}
}
