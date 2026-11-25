package model

import "fmt"

// ErrorKind is the stable, deterministic classification used for every
// failure. Classifications are part of the reproducible output: the same
// input must always produce the same ordered list of kinds.
type ErrorKind string

const (
	KindInvalidTimescale ErrorKind = "invalid_timescale" // zero/illegal timescale
	KindNegativeDuration ErrorKind = "negative_duration" // zero or negative duration
	KindOverflow         ErrorKind = "overflow"           // integer time-base overflow
	KindTruncatedMP4     ErrorKind = "truncated_mp4"      // box declares more bytes than available
	KindMalformedMP4     ErrorKind = "malformed_mp4"      // structurally invalid MP4 box
	KindBoundaryGap      ErrorKind = "boundary_gap"       // gap between adjacent segments
	KindBoundaryOverlap  ErrorKind = "boundary_overlap"   // overlapping adjacent segments
	KindMissingKeyframe  ErrorKind = "missing_keyframe"   // cut point is not a sync sample
	KindDiscontinuity    ErrorKind = "discontinuity"      // HLS discontinuity timeline jump
	KindPacketLost       ErrorKind = "packet_lost"        // unrecoverable missing RTP packet
	KindRecovered        ErrorKind = "packet_recovered"   // loss repaired inside the window
	KindEviction         ErrorKind = "eviction"           // jitter queue overflow eviction
	KindBudgetExceeded   ErrorKind = "budget_exceeded"    // processing budget exhausted
	KindSizeExceeded     ErrorKind = "size_exceeded"      // delivery size cap exceeded
)

// MediaError is a locatable failure. Offset is a byte offset for MP4 parse
// errors and a segment/sequence position elsewhere; Subsystem names the
// component that detected it so failures never get attributed ambiguously.
type MediaError struct {
	Kind      ErrorKind
	Subsystem string
	Offset    int64
	Context   string
}

// NewError builds a classified, locatable error.
func NewError(kind ErrorKind, subsystem string, offset int64, format string, args ...any) *MediaError {
	return &MediaError{Kind: kind, Subsystem: subsystem, Offset: offset,
		Context: fmt.Sprintf(format, args...)}
}

// Error implements the error interface with a stable, parseable shape.
func (e *MediaError) Error() string {
	return fmt.Sprintf("%s: [%s] @%d: %s", e.Subsystem, e.Kind, e.Offset, e.Context)
}

// Is supports errors.Is matching by kind.
func (e *MediaError) Is(target error) bool {
	t, ok := target.(*MediaError)
	return ok && t.Kind == e.Kind
}

// Locate returns the byte/position offset attached to the error, or -1 when
// the error carries no location.
func Locate(err error) int64 {
	if me, ok := err.(*MediaError); ok {
		return me.Offset
	}
	return -1
}

// Classify returns the ErrorKind of a MediaError, or an empty string for
// errors outside the taxonomy.
func Classify(err error) ErrorKind {
	if me, ok := err.(*MediaError); ok {
		return me.Kind
	}
	return ""
}
