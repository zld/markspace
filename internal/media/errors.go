// Package media defines the shared media clock, integer tick arithmetic and
// the unified error classification used by every stage of the pipeline.
package media

import (
	"fmt"
	"strings"
)

// Category is the stable, machine-readable class of a diagnostic. Categories
// are persisted in provenance records: treat them as a wire format and never
// rename one without bumping the provenance schema version.
type Category string

const (
	TruncatedContainer  Category = "truncated_container"
	CorruptSampleTable  Category = "corrupt_sample_table"
	ZeroTimescale       Category = "zero_timescale"
	NegativeDuration    Category = "negative_duration"
	Overflow            Category = "overflow"
	InvalidInput        Category = "invalid_input"
	OutOfOrderTimestamp Category = "out_of_order_timestamp"
	LateEvent           Category = "late_event"
	SequenceWrap        Category = "sequence_wrap"
	ReorderedPacket     Category = "reordered_packet"
	DuplicatePacket     Category = "duplicate_packet"
	RecoverableLoss     Category = "recoverable_loss"
	UnrecoverableGap    Category = "unrecoverable_gap"
	BoundaryNotFound    Category = "boundary_not_found"
	ReplayLimitExceeded Category = "replay_limit_exceeded"
	CheckpointCorrupt   Category = "checkpoint_corrupt"
	CheckpointAhead     Category = "checkpoint_ahead"
	IdempotencyConflict Category = "idempotency_conflict"
	InvariantViolation  Category = "invariant_violation"
)

// ValidCategory reports whether c is one of the known categories.
func ValidCategory(c Category) bool {
	_, ok := categoryStrategy[c]
	return ok
}

// Strategy names what the pipeline does when a category is observed.
type Strategy string

const (
	StrategyFail        Strategy = "fail"
	StrategySkip        Strategy = "skip"
	StrategyReorder     Strategy = "reorder"
	StrategyRecompute   Strategy = "recompute"
	StrategyResume      Strategy = "resume"
	StrategyDeduplicate Strategy = "deduplicate"
	StrategyWait        Strategy = "wait"
)

var categoryStrategy = map[Category]Strategy{
	TruncatedContainer:  StrategyFail,
	CorruptSampleTable:  StrategyFail,
	ZeroTimescale:       StrategyFail,
	NegativeDuration:    StrategyFail,
	Overflow:            StrategyFail,
	InvalidInput:        StrategyFail,
	OutOfOrderTimestamp: StrategyFail,
	LateEvent:           StrategyRecompute,
	SequenceWrap:        StrategyReorder,
	ReorderedPacket:     StrategyReorder,
	DuplicatePacket:     StrategyDeduplicate,
	RecoverableLoss:     StrategyWait,
	UnrecoverableGap:    StrategySkip,
	BoundaryNotFound:    StrategyFail,
	ReplayLimitExceeded: StrategyFail,
	CheckpointCorrupt:   StrategyResume,
	CheckpointAhead:     StrategyFail,
	IdempotencyConflict: StrategyDeduplicate,
	InvariantViolation:  StrategyFail,
}

// StrategyFor returns the default recovery strategy for a category.
func StrategyFor(c Category) Strategy {
	if s, ok := categoryStrategy[c]; ok {
		return s
	}
	return StrategyFail
}

// Error attaches a category, a human-readable message and structured location
// data (track, offset or RTP sequence number) to a failure.
type Error struct {
	Category Category
	Message  string
	Track    int32
	Offset   int64
	Seq      uint32
	HasTrack bool
	HasOff   bool
	HasSeq   bool
	Wrapped  error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Category))
	if e.HasTrack {
		fmt.Fprintf(&b, " track=%d", e.Track)
	}
	if e.HasOff {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if e.HasSeq {
		fmt.Fprintf(&b, " seq=%d", e.Seq)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Wrapped != nil {
		b.WriteString(": ")
		b.WriteString(e.Wrapped.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Wrapped }

// NewError builds a categorized error with no location.
func NewError(c Category, format string, args ...any) *Error {
	return &Error{Category: c, Message: fmt.Sprintf(format, args...)}
}

// WrapError builds a categorized error wrapping cause.
func WrapError(c Category, cause error, format string, args ...any) *Error {
	return &Error{Category: c, Message: fmt.Sprintf(format, args...), Wrapped: cause}
}

// AtTrack annotates an error with the originating track id.
func (e *Error) AtTrack(track int32) *Error {
	e.Track, e.HasTrack = track, true
	return e
}

// AtOffset annotates an error with a byte offset into the input.
func (e *Error) AtOffset(off int64) *Error {
	e.Offset, e.HasOff = off, true
	return e
}

// AtSeq annotates an error with an RTP sequence number.
func (e *Error) AtSeq(seq uint32) *Error {
	e.Seq, e.HasSeq = seq, true
	return e
}

// CategoryOf extracts the category from err, defaulting to InvariantViolation
// for unclassified errors.
func CategoryOf(err error) Category {
	var e *Error
	if As(err, &e) {
		return e.Category
	}
	return InvariantViolation
}

// As is errors.As specialized to *Error so callers need not import errors.
func As(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// IsCorruptInput reports whether err represents damaged input rather than an
// internal failure. The CLI maps corrupt input to exit status 2.
func IsCorruptInput(err error) bool {
	switch CategoryOf(err) {
	case TruncatedContainer, CorruptSampleTable, ZeroTimescale,
		NegativeDuration, Overflow, InvalidInput, OutOfOrderTimestamp,
		BoundaryNotFound:
		return true
	}
	return false
}
