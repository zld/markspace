// Package clock turns per-representation RTP publish timestamps into points on
// one shared millisecond clock.
//
// RTP timestamps are 32 bits and wrap at 2^32. The Unwrapper reconstructs a
// monotonic 64-bit timeline with RFC 3550-style wrap detection while still
// recognizing a genuine backwards jump (a publish-clock defect) instead of
// silently turning it into a wrap. pion/rtp does the wire parsing; this
// package owns the timeline math.
package clock

import (
	"github.com/pion/rtp"

	"cmafrepair/pkg/media/timebase"
)

// wrapModulus is the RTP timestamp modulus.
const wrapModulus uint64 = 1 << 32

// halfModulus decides wrap vs rollback: a forward jump larger than half the
// modulus is a wrap; a smaller backwards movement is a real rollback.
const halfModulus uint32 = 1 << 31

// Events reports what an observed 32-bit timestamp did to the timeline.
type Events struct {
	Wrapped  bool // timestamp crossed the 2^32 boundary upwards
	Rollback bool // timestamp moved backwards without a plausible wrap
	Delta    int64
}

// Unwrapper extends 32-bit RTP timestamps to a monotonic 64-bit axis.
type Unwrapper struct {
	clockHz   uint32
	started   bool
	last32    uint32
	cycles    uint64
	wraps     int
	rollbacks int
}

// NewUnwrapper requires the payload clock frequency in hertz (e.g. 90000).
func NewUnwrapper(clockHz uint32) *Unwrapper {
	return &Unwrapper{clockHz: clockHz}
}

// ClockHz returns the configured frequency.
func (u *Unwrapper) ClockHz() uint32 { return u.clockHz }

// Wraps returns the number of upward 2^32 wraparounds seen so far.
func (u *Unwrapper) Wraps() int { return u.wraps }

// Rollbacks returns the number of genuine backwards jumps seen so far.
func (u *Unwrapper) Rollbacks() int { return u.rollbacks }

// Observe feeds one raw 32-bit timestamp and returns its 64-bit extension.
func (u *Unwrapper) Observe(ts32 uint32) (uint64, Events, error) {
	if u.clockHz == 0 {
		return 0, Events{}, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseClock,
			"unwrapper has zero clock rate")
	}
	if !u.started {
		u.started = true
		u.last32 = ts32
		return uint64(ts32), Events{}, nil
	}
	ev := Events{}
	switch {
	case ts32 == u.last32:
		ev.Delta = 0
	case ts32 > u.last32:
		ev.Delta = int64(ts32 - u.last32)
	default: // ts32 < last32
		gap := u.last32 - ts32
		if gap > halfModulus {
			// Forward wrap: e.g. 0xFFFFFFF0 -> 0x00000010.
			u.cycles += wrapModulus
			u.wraps++
			ev.Wrapped = true
			ev.Delta = int64(wrapModulus - uint64(gap))
		} else {
			// Real backwards jump; keep the cycle count unchanged.
			u.rollbacks++
			ev.Rollback = true
			ev.Delta = -int64(gap)
		}
	}
	u.last32 = ts32
	return u.cycles + uint64(ts32), ev, nil
}

// ClockAnchor pairs one extended RTP instant with one shared-clock instant
// for a single representation (SSRC). MediaToRTP is the constant offset
// between the track media timeline (tfdt domain) and the RTP clock, both
// measured at ClockHz: extendedRTP = mediaTime + MediaToRTP.
type ClockAnchor struct {
	SSRC           uint32
	ClockHz        uint32
	AnchorRTP      uint64
	AnchorSharedMs int64
	MediaToRTP     int64
}

// MediaToShared maps a track media time to whole shared-clock milliseconds
// through the anchor, rounded half away from zero.
func (a ClockAnchor) MediaToShared(mediaTime uint64) (int64, error) {
	if a.ClockHz == 0 {
		return 0, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseClock,
			"anchor has zero clock rate")
	}
	extRTP, err := saturatingAdd(int64(mediaTime), a.MediaToRTP)
	if err != nil {
		return 0, err
	}
	elapsedTicks := extRTP - int64(a.AnchorRTP)
	elapsedMs, err := timebase.ToSharedMs(uint64(abs64c(elapsedTicks)), a.ClockHz)
	if err != nil {
		return 0, err
	}
	if elapsedTicks < 0 {
		elapsedMs = -elapsedMs
	}
	return a.AnchorSharedMs + elapsedMs, nil
}

// RTPToShared maps an extended RTP timestamp directly.
func (a ClockAnchor) RTPToShared(extRTP uint64) (int64, error) {
	if a.ClockHz == 0 {
		return 0, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseClock,
			"anchor has zero clock rate")
	}
	delta := int64(extRTP) - int64(a.AnchorRTP)
	ms, err := timebase.ToSharedMs(uint64(abs64c(delta)), a.ClockHz)
	if err != nil {
		return 0, err
	}
	if delta < 0 {
		ms = -ms
	}
	return a.AnchorSharedMs + ms, nil
}

// ParsePacket decodes one RTP datagram with pion/rtp.
func ParsePacket(raw []byte) (*rtp.Packet, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, timebase.NewError(timebase.CodeInputInvalid, timebase.PhaseClock,
			"rtp unmarshal: %v", err)
	}
	return pkt, nil
}

func abs64c(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// saturatingAdd adds two signed values with an overflow guard.
func saturatingAdd(a, b int64) (int64, error) {
	r := a + b
	if (r^a)&(r^b) < 0 {
		return 0, timebase.NewError(timebase.CodeClockDivergence, timebase.PhaseClock,
			"clock addition overflow")
	}
	return r, nil
}
