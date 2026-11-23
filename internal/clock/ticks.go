// Package clock defines the unified rational time base shared by every
// pipeline stage, plus the pipeline-wide error model.
//
// All tracks (H.264/H.265/AV1) are mapped onto a single int64 microsecond
// scale (Usec) so that timeline merge never compares raw RTP clock ticks
// from different rate domains.
package clock

import (
	"errors"
	"math/bits"
)

// Phase names carried by Error across the whole pipeline.
const (
	PhaseTransport = "transport"
	PhaseReorder   = "reorder"
	PhaseJournal   = "journal"
	PhaseCodec     = "codec"
	PhaseIndex     = "index"
	PhaseTimeline  = "timeline"
	PhaseExport    = "export"
)

// Error is the uniform error model: every stage failure carries the track
// id, the packet sequence it relates to (or -1), the pipeline phase and
// whether the caller may keep running after handling it.
type Error struct {
	Track       uint32
	Seq         int
	Phase       string
	Recoverable bool
	Message     string
	Cause       error
}

func (e *Error) Error() string {
	msg := "phase=" + e.Phase + " track=" + u32(e.Track) +
		" seq=" + itoa(int64(e.Seq)) + " recoverable=" + b(e.Recoverable) + ": " + e.Message
	if e.Cause != nil {
		msg += " (" + e.Cause.Error() + ")"
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Cause }

// E builds a pipeline Error with the given context.
func E(phase string, track uint32, seq int, recoverable bool, msg string, cause error) *Error {
	return &Error{Phase: phase, Track: track, Seq: seq, Recoverable: recoverable, Message: msg, Cause: cause}
}

// Rate is a rational tick rate: Num ticks per Den seconds of wall time.
type Rate struct {
	Num int64 // ticks
	Den int64 // seconds
}

// Common rate domains.
var (
	Usec     = Rate{Num: 1_000_000, Den: 1}
	RTPVideo = Rate{Num: 90_000, Den: 1}
	Msec     = Rate{Num: 1_000, Den: 1}
	Seconds  = Rate{Num: 1, Den: 1}
)

// Validate rejects zero/negative denominators and non-positive numerators.
func (r Rate) Validate() error {
	if r.Den <= 0 {
		return E(PhaseTimeline, 0, -1, false, "rate denominator must be positive", nil)
	}
	if r.Num <= 0 {
		return E(PhaseTimeline, 0, -1, false, "rate numerator must be positive", nil)
	}
	return nil
}

// Convert maps ticks at rate from into ticks at rate to.
// A zero denominator, negative tick or 64-bit overflow produces a context
// error rather than a wrapped result.
func Convert(ticks int64, from, to Rate) (int64, error) {
	if err := from.Validate(); err != nil {
		return 0, err
	}
	if err := to.Validate(); err != nil {
		return 0, err
	}
	if ticks < 0 {
		return 0, E(PhaseTimeline, 0, -1, false, "negative tick cannot be converted", nil)
	}
	// out = ticks * to.Num * from.Den / (from.Num * to.Den)
	num, ok1 := product3(ticks, to.Num, from.Den)
	den, ok2 := product2(from.Num, to.Den)
	if !ok1 || !ok2 || den <= 0 {
		return 0, E(PhaseTimeline, 0, -1, false, "time conversion overflow", nil)
	}
	return num / den, nil
}

// product2 reports a*b only when it fits in a positive int64.
func product2(a, b int64) (int64, bool) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > uint64(maxInt64) {
		return 0, false
	}
	return int64(lo), true
}

// product3 reports a*b*c with overflow checked at every multiplication.
func product3(a, b, c int64) (int64, bool) {
	ab, ok := product2(a, b)
	if !ok {
		return 0, false
	}
	return product2(ab, c)
}

const maxInt64 = int64(^uint64(0) >> 1)

// Unwrap32 extends a stream of uint32 RTP timestamps into a monotonic int64
// domain, handling 2^32 wraparound and non-zero origins. Call Begin32 for the
// first sample, then Unwrap32 for each following one.
func Begin32(first uint32) int64 { return int64(first) }

// Unwrap32 advances the unwrapped stream; prev is the previous unwrapped
// value, raw the next on-wire timestamp.
//
// The step is interpreted as a signed 32-bit forward step, so crossing
// 0xFFFFFFFF -> 0 advances by one instead of reading as a huge backward
// jump. A step that is negative at that scale is a genuine regression (for
// example a node restart reusing a smaller clock base); absorbing it would
// silently shift the entire remaining timeline, so it is a context error.
func Unwrap32(prev int64, raw uint32) (int64, error) {
	delta := int64(int32(raw - uint32(prev)))
	if delta < 0 {
		return 0, E(PhaseTimeline, 0, -1, false,
			"timestamp moved backwards: clock regression is not a wraparound", nil)
	}
	next := prev + delta
	if next < 0 {
		return 0, E(PhaseTimeline, 0, -1, false, "unwrapped timestamp is negative", nil)
	}
	return next, nil
}

// Unwrap16 extends uint16 RTP sequence numbers (including 65535->0 wrap)
// into an int64 monotonic domain.
func Unwrap16(prev int64, raw uint16) int64 {
	delta := int64(int16(raw - uint16(prev)))
	return prev + delta
}

// AsError unwraps into *Error if err is one.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// small local helpers keep the package allocation-free on the error path.
func u32(v uint32) string { return itoa(int64(v)) }

func b(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
