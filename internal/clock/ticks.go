// Package clock implements integer-only media-clock math shared by the
// metadata parsers and the stream pipeline: timescale conversion with
// remainder tracking, RTP timestamp unwrap, RTP sequence classification and
// master-clock segment alignment. No floating point is ever used for time
// accumulation, so repeated runs over the same input produce identical
// integer timelines.
package clock

import (
	"fmt"
	"time"
)

// Stage identifiers carried by Error so failures can be traced across modules.
const (
	StageMP4      = "meta-mp4"
	StageMXF      = "meta-mxf"
	StageClock    = "clock"
	StageStream   = "stream"
	StageManifest = "manifest"
)

// Error is the shared error type of the parser and stream stages. It records
// the originating stage, the track/layer (zero when not applicable) and
// whether the pipeline can keep running with the affected layer isolated.
type Error struct {
	Stage       string
	Track       uint32
	Recoverable bool
	Reason      string
	Err         error
}

// NewError builds a staged error without a wrapped cause.
func NewError(stage, reason string, recoverable bool) *Error {
	return &Error{Stage: stage, Reason: reason, Recoverable: recoverable}
}

// TrackError builds a staged error attributed to a specific track/layer.
func TrackError(stage string, track uint32, reason string, recoverable bool) *Error {
	return &Error{Stage: stage, Track: track, Reason: reason, Recoverable: recoverable}
}

// WrapError annotates a lower-level error with stage and track metadata.
func WrapError(stage string, track uint32, recoverable bool, err error) *Error {
	return &Error{Stage: stage, Track: track, Recoverable: recoverable, Err: err}
}

func (e *Error) Error() string {
	scope := e.Stage
	if e.Track != 0 {
		scope = fmt.Sprintf("%s/track%d", e.Stage, e.Track)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", scope, e.Reason, e.Err)
	}
	return fmt.Sprintf("%s: %s", scope, e.Reason)
}

// Unwrap exposes the wrapped cause for errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Clock is a fixed-rate integer master clock: ticks = seconds * Timescale.
type Clock struct {
	Timescale uint32
}

// New returns a clock for the given ticks-per-second timescale.
func New(timescale uint32) (*Clock, error) {
	if timescale == 0 {
		return nil, NewError(StageClock, "zero timescale", false)
	}
	return &Clock{Timescale: timescale}, nil
}

// Ticks converts a wall-clock duration to an integer tick count, rounding
// half away from zero.
func (c *Clock) Ticks(d time.Duration) int64 {
	num := d.Nanoseconds() * int64(c.Timescale)
	den := int64(time.Second)
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// Millis converts a tick count to whole milliseconds (truncated).
func (c *Clock) Millis(ticks int64) int64 {
	return ticks * 1000 / int64(c.Timescale)
}

// SegmentTicks is the fixed integer length of one segment on this clock.
func (c *Clock) SegmentTicks(segDur time.Duration) int64 {
	t := c.Ticks(segDur)
	if t <= 0 {
		return 0
	}
	return t
}

// AlignDown rounds ticks down to the nearest multiple of boundary.
func AlignDown(ticks, boundary int64) int64 {
	if boundary <= 0 {
		return ticks
	}
	return ticks - modNonNeg(ticks, boundary)
}

// AlignUp rounds ticks up to the nearest multiple of boundary.
func AlignUp(ticks, boundary int64) int64 {
	if boundary <= 0 {
		return ticks
	}
	m := modNonNeg(ticks, boundary)
	if m == 0 {
		return ticks
	}
	return ticks + boundary - m
}

func modNonNeg(x, m int64) int64 {
	r := x % m
	if r < 0 {
		r += m
	}
	return r
}

// Scale converts a single timestamp between integer timescales using round
// half up. Stream conversion should use Converter, which retains the
// truncation remainder and guarantees a monotonic output timeline.
func Scale(ts int64, srcScale, dstScale uint32) (int64, error) {
	if srcScale == 0 || dstScale == 0 {
		return 0, NewError(StageClock, "zero timescale in Scale", false)
	}
	num := ts * int64(dstScale)
	den := int64(srcScale)
	if num >= 0 {
		return (num + den/2) / den, nil
	}
	return -((-num + den/2) / den), nil
}

// Converter maps a monotonic integer timeline from one timescale to another.
// A running remainder distributes non-divisible ticks instead of dropping
// them, so the destination timeline never drifts and never goes backwards.
type Converter struct {
	src       uint32
	dst       uint32
	haveBase  bool
	lastSrc   int64
	lastDst   int64
	remainder int64
	// Duplicates counts repeated source timestamps.
	Duplicates int64
	// Reordered counts timestamps older than the last accepted one.
	Reordered int64
}

// NewConverter builds a converter between two non-zero timescales.
func NewConverter(srcScale, dstScale uint32) (*Converter, error) {
	if srcScale == 0 || dstScale == 0 {
		return nil, NewError(StageClock, "zero timescale in converter", false)
	}
	return &Converter{src: srcScale, dst: dstScale}, nil
}

// Push maps one unwrapped source timestamp onto the destination timescale.
// Duplicate and out-of-order inputs return a recoverable clock.Error and the
// last valid destination timestamp; the output invariant stays monotonic.
func (c *Converter) Push(srcTS int64) (int64, error) {
	if !c.haveBase {
		c.haveBase = true
		c.lastSrc = srcTS
		return 0, nil
	}
	diff := srcTS - c.lastSrc
	switch {
	case diff == 0:
		c.Duplicates++
		return c.lastDst, NewError(StageClock, "duplicate timestamp", true)
	case diff < 0:
		c.Reordered++
		return c.lastDst, NewError(StageClock, "timestamp moved backwards", true)
	}
	scaled := diff*int64(c.dst) + c.remainder
	out := scaled / int64(c.src)
	c.remainder = scaled - out*int64(c.src)
	c.lastSrc = srcTS
	c.lastDst += out
	return c.lastDst, nil
}

// rtpMod is the modulus of an RTP 32-bit timestamp.
const rtpMod = int64(1) << 32

// Unwrapper expands modular RTP timestamps into a monotonic int64 timeline,
// applying RFC 3550 signed-delta wraparound arithmetic.
type Unwrapper struct {
	have bool
	last uint64
	// Duplicates counts timestamps equal to the previous one.
	Duplicates int64
	// Backwards counts timestamps whose signed delta is negative.
	Backwards int64
}

// NewRTPUnwrapper creates an empty RTP timestamp unwrapper.
func NewRTPUnwrapper() *Unwrapper { return &Unwrapper{} }

// Next unwraps one 32-bit RTP timestamp. A duplicate or backwards input
// yields a recoverable error and the previous expanded value, keeping the
// emitted timeline monotonic.
func (u *Unwrapper) Next(ts uint32) (int64, error) {
	if !u.have {
		u.have = true
		u.last = uint64(ts)
		return int64(u.last), nil
	}
	delta := int64(int32(ts - uint32(u.last)))
	switch {
	case delta > 0:
		u.last += uint64(delta)
	case delta == 0:
		u.Duplicates++
		return int64(u.last), NewError(StageClock, "duplicate RTP timestamp", true)
	default:
		u.Backwards++
		return int64(u.last), NewError(StageClock, "RTP timestamp moved backwards", true)
	}
	if u.last > uint64(rtpMod)<<20 {
		return 0, NewError(StageClock, "unwrapped timestamp overflow", false)
	}
	return int64(u.last), nil
}

// Packet status flags returned by SeqTracker.Next.
const (
	StatusOK        = 0
	StatusDuplicate = 1 << iota
	StatusReordered
)

// SeqTracker classifies 16-bit RTP sequence numbers and counts gaps so loss,
// duplication and reordering are reproducible per layer.
type SeqTracker struct {
	have       bool
	last       uint16
	Lost       int64
	Duplicates int64
	Reordered  int64
}

// Next records a sequence number, reports its status flags and the number of
// packets lost in the gap before it (zero for duplicates/reorders).
func (s *SeqTracker) Next(seq uint16) (status, lost int) {
	if !s.have {
		s.have = true
		s.last = seq
		return StatusOK, 0
	}
	delta := int16(seq - s.last)
	switch {
	case delta > 0:
		lost = int(delta) - 1
		s.Lost += int64(lost)
		s.last = seq
		return StatusOK, lost
	case delta == 0:
		s.Duplicates++
		return StatusDuplicate, 0
	default:
		s.Reordered++
		return StatusReordered, 0
	}
}
