// Package clock provides the unified media time base shared by every
// downstream stage: rational media-scale timestamps, 32-bit wrap-around
// unwrapping, RTP packet-boundary handling and linear node-clock drift
// estimation. It also owns the canonical error model used across packages.
package clock

import (
	"fmt"
)

// ErrorCode enumerates the deterministic error classes the pipeline reports.
type ErrorCode string

const (
	// ErrTruncatedBox signals a container box that ends before its payload.
	ErrTruncatedBox ErrorCode = "TRUNCATED_BOX"
	// ErrIllegalOffset signals a sample offset outside the file/mdat range.
	ErrIllegalOffset ErrorCode = "ILLEGAL_OFFSET"
	// ErrTimestampReversal signals a non-monotonic timestamp that cannot be
	// explained by wrap-around or late replay.
	ErrTimestampReversal ErrorCode = "TIMESTAMP_REVERSAL"
	// ErrCRC signals a payload CRC mismatch on a generated PCM sample.
	ErrCRC ErrorCode = "CRC_MISMATCH"
	// ErrCheckpointCorrupt signals a checkpoint that fails its integrity tag.
	ErrCheckpointCorrupt ErrorCode = "CHECKPOINT_CORRUPT"
	// ErrChannelPolicy signals a layout mapping policy violation.
	ErrChannelPolicy ErrorCode = "CHANNEL_POLICY"
	// ErrInvariant signals a failed cross-stage invariant check.
	ErrInvariant ErrorCode = "INVARIANT"
	// ErrUnrecoverable signals damage from which no valid restart point exists.
	ErrUnrecoverable ErrorCode = "UNRECOVERABLE"
)

// Error is the canonical structured error. Every recoverable media error
// carries the file offset, track id and sample index needed to localize it;
// unknown positions use -1.
type Error struct {
	Code        ErrorCode
	FileOffset  int64
	TrackID     uint32
	SampleIndex int64
	Message     string
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (offset=%d track=%d sample=%d)",
		e.Code, e.Message, e.FileOffset, e.TrackID, e.SampleIndex)
}

// NewError builds a clock.Error with -1 sentinels for unknown positions.
func NewError(code ErrorCode, msg string) *Error {
	return &Error{Code: code, FileOffset: -1, TrackID: 0, SampleIndex: -1, Message: msg}
}

// ErrInvalidRTP wraps an RTP serialization failure.
func ErrInvalidRTP(msg string) *Error {
	return NewError(ErrInvariant, "rtp: "+msg)
}

// WithPosition returns a copy annotated with the damage position.
func (e *Error) WithPosition(offset int64, track uint32, sample int64) *Error {
	cp := *e
	cp.FileOffset, cp.TrackID, cp.SampleIndex = offset, track, sample
	return &cp
}

// Recoverable reports whether the pipeline may skip the damaged sample and
// continue from the latest checkpoint.
func (e *Error) Recoverable() bool {
	switch e.Code {
	case ErrTruncatedBox, ErrIllegalOffset, ErrCRC, ErrTimestampReversal, ErrChannelPolicy:
		return true
	default:
		return false
	}
}

// Rational is an exact rational number, used for media timescales so no
// floating-point rounding accumulates across long streams.
type Rational struct {
	Num uint64
	Den uint64
}

// ScaleExact converts a tick count in scale (num/den) into a tick count in
// the receiver's scale. The intermediate product can exceed 64 bits, so the
// result is computed in 128-bit arithmetic and must fit uint64.
func (r Rational) ScaleExact(ticks uint64, from Rational) uint64 {
	hi, lo := mul128(ticks, from.Num*r.Den)
	den := from.Den * r.Num
	q, _ := div128(hi, lo, den)
	return q
}

// MediaClock binds a track's integer timescale (ticks per second) to the
// canonical nanosecond media scale while retaining exact rational math.
type MediaClock struct {
	timescale uint32
	tickUnit  Rational // size of one track tick, in nanoseconds
}

// NewMediaClock builds the clock for a track. timescale must be positive.
func NewMediaClock(timescale uint32) (*MediaClock, error) {
	if timescale == 0 {
		return nil, NewError(ErrInvariant, "timescale must be non-zero")
	}
	return &MediaClock{
		timescale: timescale,
		tickUnit:  Rational{Num: 1e9, Den: uint64(timescale)},
	}, nil
}

// Timescale returns the source track timescale.
func (c *MediaClock) Timescale() uint32 { return c.timescale }

// ToNanos converts track ticks to exact nanoseconds.
func (c *MediaClock) ToNanos(ticks uint64) uint64 {
	return (Rational{Num: 1, Den: 1}).ScaleExact(ticks, c.tickUnit)
}

// FromNanos converts canonical nanoseconds back to track ticks (floor).
func (c *MediaClock) FromNanos(ns uint64) uint64 {
	// One nanosecond equals timescale/1e9 track ticks.
	nsUnit := Rational{Num: uint64(c.timescale), Den: 1e9}
	return (Rational{Num: 1, Den: 1}).ScaleExact(ns, nsUnit)
}
