// Package clock defines the unified media clock used by the archivist
// pipeline. It converts ticks between track and movie timescales and expands
// 32-bit RTP-style sample timestamps into a monotonic 64-bit timeline.
package clock

import (
	"errors"
	"math/big"

	"github.com/pion/rtp"
)

// Wrap is the modulus of an RTP-style 32-bit timestamp.
const Wrap = int64(1) << 32

// Version identifies the clock conversion rules stored in provenance data.
const Version = "clock/v1"

// ErrZeroTimescale is returned when a track or movie timescale is zero.
var ErrZeroTimescale = errors.New("timescale must be greater than zero")

// Timebase is a media timescale expressed in ticks per second.
type Timebase struct {
	Timescale uint32
}

// New validates and returns a Timebase.
func New(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, ErrZeroTimescale
	}
	return Timebase{Timescale: timescale}, nil
}

// ScaleTicks converts v from one timescale to another using exact integer
// arithmetic and half-away-from-zero rounding. Results stay deterministic
// across platforms and runs.
func ScaleTicks(v int64, from, to uint32) int64 {
	if from == 0 || to == 0 || from == to || v == 0 {
		return v
	}
	num := big.NewInt(v)
	num.Mul(num, big.NewInt(int64(to)))
	den := big.NewInt(int64(from))
	rem := new(big.Int)
	num.QuoRem(num, den, rem)
	twice := new(big.Int).Lsh(new(big.Int).Abs(rem), 1)
	if twice.Cmp(den) >= 0 {
		if v > 0 {
			num.Add(num, big.NewInt(1))
		} else {
			num.Sub(num, big.NewInt(1))
		}
	}
	return num.Int64()
}

// ToMedia converts ticks from the receiver's timescale to media ticks.
func (tb Timebase) ToMedia(v int64, mediaTimescale uint32) int64 {
	return ScaleTicks(v, tb.Timescale, mediaTimescale)
}

// FromMedia converts media ticks into the receiver's timescale.
func (tb Timebase) FromMedia(v int64, mediaTimescale uint32) int64 {
	return ScaleTicks(v, mediaTimescale, tb.Timescale)
}

// SecondsToTicks converts whole seconds into ticks of the receiver.
func (tb Timebase) SecondsToTicks(seconds int64) int64 {
	if tb.Timescale == 0 {
		return 0
	}
	return seconds * int64(tb.Timescale)
}

// RawFromHeader extracts the 32-bit timestamp carried by an RTP header.
// pion/rtp supplies the canonical wire representation of RTP timestamps.
func RawFromHeader(h rtp.Header) uint32 {
	return h.Timestamp
}

// HeaderForRaw builds an RTP header carrying raw as its timestamp.
func HeaderForRaw(raw uint32) rtp.Header {
	return rtp.Header{Version: 2, Timestamp: raw}
}

// SignedDelta returns the modulo-2^32 difference between two raw
// timestamps interpreted as a signed 32-bit value, following the RTP
// timestamp comparison rules from RFC 3550.
func SignedDelta(raw, prev uint32) int32 {
	return int32(raw - prev)
}

// Extend maps raw onto the 64-bit timeline closest to prev. The first bool
// reports whether the forward-moving interpretation crossed a wrap boundary,
// so a sample that continues the cadence across 0xFFFFFFFF is flagged while a
// backward jump near the boundary is not.
func Extend(prev int64, raw uint32) (ext int64, wrapped bool) {
	base := prev &^ (Wrap - 1)
	cand := base | int64(raw)
	delta := cand - prev
	switch {
	case delta > Wrap/2:
		return cand - Wrap, false
	case delta < -Wrap/2:
		return cand + Wrap, true
	default:
		return cand, false
	}
}

// Unwrapper expands a sequence of raw 32-bit timestamps. The first Push seeds
// the timeline directly from the raw value; later values are unfolded using
// the nearest-candidate rule.
type Unwrapper struct {
	prev    int64
	started bool
	wraps   int
}

// Push expands one raw timestamp.
func (u *Unwrapper) Push(raw uint32) (ext int64, wrapped bool) {
	if !u.started {
		u.started = true
		u.prev = int64(raw)
		return u.prev, false
	}
	ext, wrapped = Extend(u.prev, raw)
	if wrapped {
		u.wraps++
	}
	u.prev = ext
	return ext, wrapped
}

// Wraps reports how many wrap boundaries have been crossed.
func (u *Unwrapper) Wraps() int { return u.wraps }

// Rebase returns v relative to base. Values before base are preserved as
// negative offsets so callers can detect an impossible ordering.
func Rebase(base, v int64) int64 { return v - base }

// WrapShift reports how many ticks were added to raw to produce ext. It is
// zero for values on the first revolution and a multiple of 2^32 afterwards.
func WrapShift(raw uint32, ext int64) int64 { return ext - int64(raw) }
