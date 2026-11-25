// Package timeline defines the shared rational time base, sample model and
// error model used by the ingest, fingerprint, archive and recovery
// subsystems. All wall-clock quantities are kept as exact rationals so that
// VFR material is never approximated through an average frame rate.
package timeline

import (
	"errors"
	"fmt"
)

// TrackKind identifies the media kind carried by a track.
type TrackKind uint8

const (
	KindUnknown TrackKind = 0
	KindVideo   TrackKind = 1
	KindAudio   TrackKind = 2
)

func (k TrackKind) String() string {
	switch k {
	case KindVideo:
		return "video"
	case KindAudio:
		return "audio"
	default:
		return "unknown"
	}
}

// TrackID identifies a track inside a named source.
type TrackID struct {
	Source string
	Index  uint32
	Kind   TrackKind
}

func (t TrackID) String() string {
	return fmt.Sprintf("%s/t%d(%s)", t.Source, t.Index, t.Kind)
}

// ClockRef records the originating clock of a timestamp: the source name plus
// the clock identifier (RTP SSRC or the MP4 track timescale owner).
type ClockRef struct {
	Source string
	Clock  uint32
}

// Rational is an exact signed time quantity expressed as Num/Den seconds.
// Den is always strictly greater than zero.
type Rational struct {
	Num int64
	Den uint32
}

// FromTicks converts a raw tick count on a clock running at ticksPerSecond
// into a Rational time in seconds.
func FromTicks(ticks int64, ticksPerSecond uint32) Rational {
	if ticksPerSecond == 0 {
		return Rational{Num: 0, Den: 1}
	}
	return Rational{Num: ticks, Den: ticksPerSecond}.Normalize()
}

// Normalize reduces the fraction and moves the sign onto Num.
func (r Rational) Normalize() Rational {
	if r.Den == 0 {
		return Rational{Num: 0, Den: 1}
	}
	if r.Num == 0 {
		return Rational{Num: 0, Den: 1}
	}
	sign := int64(1)
	n := r.Num
	if n < 0 {
		n = -n
		sign = -1
	}
	d := int64(r.Den)
	g := gcd(n, d)
	return Rational{Num: sign * n / g, Den: uint32(d / g)}
}

// Nanoseconds renders the time as an integer nanosecond value, truncating
// towards zero.
func (r Rational) Nanoseconds() int64 {
	if r.Den == 0 {
		return 0
	}
	return r.Num * 1_000_000_000 / int64(r.Den)
}

// Cmp returns -1, 0 or 1 comparing two exact rationals.
func (r Rational) Cmp(o Rational) int {
	a := r.Num * int64(o.Den)
	b := o.Num * int64(r.Den)
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Add returns the exact sum of two rationals.
func (r Rational) Add(o Rational) Rational {
	return Rational{
		Num: r.Num*int64(o.Den) + o.Num*int64(r.Den),
		Den: r.Den * o.Den,
	}.Normalize()
}

// Sub returns the exact difference of two rationals.
func (r Rational) Sub(o Rational) Rational {
	return Rational{
		Num: r.Num*int64(o.Den) - o.Num*int64(r.Den),
		Den: r.Den * o.Den,
	}.Normalize()
}

// Abs returns the absolute value of the rational.
func (r Rational) Abs() Rational {
	if r.Num < 0 {
		return Rational{Num: -r.Num, Den: r.Den}
	}
	return r
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// Timestamp preserves both the raw clock ticks and the normalized rational
// DTS/PTS, together with the track and source clock they came from.
type Timestamp struct {
	Track      TrackID
	Clock      ClockRef
	RawDTS     int64
	RawPTS     int64
	Timescale  uint32
	DTS        Rational
	PTS        Rational
	WrapCount  uint32 // number of 32-bit RTP wraps unwrapped before this point
	Order      uint64 // packet/sample position on the originating track
}

// Sample is the normalized unit flowing between stages: a time-stamped piece
// of media payload plus the references needed to trace it back to its origin.
type Sample struct {
	Track   TrackID
	Order   uint64
	TS      Timestamp
	Payload []byte
	Marker  bool
}

// Stage names shared by every subsystem.
const (
	StageIngest      = "ingest"
	StageNormalize   = "normalize"
	StageFingerprint = "fingerprint"
	StageArchive     = "archive"
)

// Error kinds emitted by the pipeline.
const (
	KindTruncatedBox      = "truncated-box"
	KindIllegalBoxLength  = "illegal-box-length"
	KindDuplicateTime     = "duplicate-timestamp"
	KindMissingTrack      = "missing-track"
	KindBoundaryJump      = "boundary-jump"
	KindTruncatedCapture  = "truncated-capture"
	KindUnrecoverable     = "unrecoverable-track"
)

// StageError is the common error model. It records the failing stage, the
// byte offset or packet/sample position where it was observed, the owning
// track and whether re-running from a checkpoint can recover.
type StageError struct {
	Stage       string
	Kind        string
	Offset      int64
	Position    uint64
	Track       TrackID
	Recoverable bool
	Detail      string
	Err         error
}

func (e *StageError) Error() string {
	loc := fmt.Sprintf("offset=%d", e.Offset)
	if e.Position > 0 {
		loc = fmt.Sprintf("position=%d", e.Position)
	}
	return fmt.Sprintf("%s/%s at %s track=%s recoverable=%t: %s",
		e.Stage, e.Kind, loc, e.Track, e.Recoverable, e.detail())
}

func (e *StageError) detail() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Detail
}

// Unwrap exposes the wrapped error for errors.Is/As.
func (e *StageError) Unwrap() error { return e.Err }

// NewStageError builds a StageError without a wrapped cause.
func NewStageError(stage, kind string, offset int64, recoverable bool, detail string) *StageError {
	return &StageError{Stage: stage, Kind: kind, Offset: offset, Recoverable: recoverable, Detail: detail}
}

// ErrTrackFailed marks a track that could not be repaired. Other tracks must
// keep being processed when this error is returned.
var ErrTrackFailed = errors.New("track failed and cannot be repaired")
