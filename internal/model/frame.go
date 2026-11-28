// Package model defines the shared media frame, time base and error model
// used by every subsystem (ingest, rtp, mp4, timeline, index, repair, report).
package model

import (
	"errors"
	"math/big"
)

// FrameType enumerates the kinds of frames carried on a track.
type FrameType uint8

const (
	TypeUnknown FrameType = iota
	TypeKeyFrame
	TypeDelta
)

// String renders the frame type for reports and traces.
func (t FrameType) String() string {
	switch t {
	case TypeKeyFrame:
		return "key"
	case TypeDelta:
		return "delta"
	default:
		return "unknown"
	}
}

// ErrorKind classifies a CodeError so callers never parse strings.
type ErrorKind uint8

const (
	KindNone ErrorKind = iota
	KindChecksum
	KindGap
	KindTruncatedBox
	KindTimeBase
	KindLength
)

// String renders the error kind.
func (k ErrorKind) String() string {
	switch k {
	case KindChecksum:
		return "checksum"
	case KindGap:
		return "gap"
	case KindTruncatedBox:
		return "truncated_box"
	case KindTimeBase:
		return "time_base"
	case KindLength:
		return "length"
	default:
		return "none"
	}
}

// RawOffset records where a logical record came from inside the byte stream.
// Every parsed frame keeps the offset of the record it was derived from so the
// whole pipeline can be traced back to the original input.
type RawOffset struct {
	// Stream identifies the input byte stream (e.g. a generated case name).
	Stream string
	// Start is the byte offset of the record header.
	Start int64
	// End is the byte offset immediately after the record.
	End int64
}

// CodeError is the single error model shared by all subsystems. It pins the
// failure to a kind, an original offset and an optional track/sequence.
type CodeError struct {
	Kind     ErrorKind
	Track    uint32
	Source   uint32
	Seq      uint32
	Offset   RawOffset
	Message  string
	FirstPos int64 // first byte position at which the defect is observable
}

// Error implements the error interface.
func (e *CodeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Kind.String() + ": " + e.Message
}

// AsCodeError extracts a *CodeError from err, wrapping nothing into nil.
func AsCodeError(err error) *CodeError {
	var ce *CodeError
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

// Rational is an exact rational time base (ticks per second). It never uses
// floating point so conversions are repeatable across runs and orderings.
type Rational struct {
	Num uint64 // ticks per ...
	Den uint64 // ... Den seconds
}

// Valid reports whether the rational can be used as a time base.
func (r Rational) Valid() bool { return r.Num > 0 && r.Den > 0 }

// TickMicros converts an integer tick count under this base to microseconds.
// It returns false when the multiplication would overflow an int64.
func (r Rational) TickMicros(ticks int64) (int64, bool) {
	if !r.Valid() || ticks < 0 {
		return 0, false
	}
	num := new(big.Int).SetInt64(ticks)
	num.Mul(num, big.NewInt(1_000_000))
	num.Mul(num, new(big.Int).SetUint64(r.Den))
	den := new(big.Int).SetUint64(r.Num)
	num.Quo(num, den)
	if !num.IsInt64() {
		return 0, false
	}
	return num.Int64(), true
}

// Timestamp keeps every representation required by the spec: the original tick
// and scale, the normalized microsecond value and the source byte offset.
type Timestamp struct {
	Ticks     int64
	Base      Rational
	Micros    int64
	HasMicros bool
	Offset    RawOffset
}

// MicrosOr returns the normalized microsecond value, or fallback if unknown.
func (t Timestamp) MicrosOr(fallback int64) int64 {
	if t.HasMicros {
		return t.Micros
	}
	return fallback
}

// Frame is one validated or invalidated media frame. Invalid frames are still
// represented (never silently dropped) so damage segments can be located.
type Frame struct {
	Track      uint32
	Source     uint32
	Seq        uint32
	EventTime  Timestamp
	PayloadLen uint32
	Type       FrameType
	Offset     RawOffset
	CRC        uint32
	Valid      bool
	Err        *CodeError
}

// NewTimestamp builds a Timestamp and normalizes to microseconds when possible.
func NewTimestamp(ticks int64, base Rational, off RawOffset) Timestamp {
	ts := Timestamp{Ticks: ticks, Base: base, Offset: off}
	if micros, ok := base.TickMicros(ticks); ok {
		ts.Micros, ts.HasMicros = micros, true
	}
	return ts
}
