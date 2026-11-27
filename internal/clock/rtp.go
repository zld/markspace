package clock

import (
	"fmt"

	"github.com/pion/rtp"
)

// RTPClock maps a live source's 32-bit RTP timestamps onto exact programme
// time. RTP timestamps are unsigned and wrap; this clock unwraps them with
// signed 32-bit deltas so a source can stream for days without drift and so
// small reordering is tolerated.
type RTPClock struct {
	rate   uint32
	base   Rational // programme time of extended tick zero
	last   uint32   // last raw timestamp observed
	ext    uint64   // unwrapped tick counter
	primed bool
}

// NewRTPClock anchors a source at programme time at with raw timestamp first.
func NewRTPClock(rate uint32, first uint32, at Rational) (*RTPClock, error) {
	if rate == 0 {
		return nil, fmt.Errorf("%w: RTP clock rate", ErrOverflow)
	}
	firstTicks, err := at.TicksExpr(int64(rate))
	if err != nil {
		return nil, err
	}
	base, err := Make(at.N*int64(rate)-firstTicks*at.D, at.D*int64(rate))
	if err != nil {
		return nil, err
	}
	return &RTPClock{rate: rate, base: base, last: first, ext: uint64(firstTicks), primed: true}, nil
}

// Rate returns the clock rate in ticks per second.
func (c *RTPClock) Rate() uint32 { return c.rate }

// Advance consumes a timestamp and returns its exact programme time. Passing
// the timestamp of a previously seen RTP packet is allowed as long as the
// packet is not more than half the 32-bit range behind the last one.
func (c *RTPClock) Advance(ts uint32) (Rational, error) {
	if !c.primed {
		ticks, err := c.base.TicksExpr(int64(c.rate))
		if err != nil {
			return Rational{}, err
		}
		c.primed = true
		c.last = ts
		c.ext = uint64(ticks)
	}
	delta := int64(int32(ts - c.last)) // two's-complement wrap handling
	if delta < 0 {
		// A reordered packet: map it relative to the high-water mark without
		// moving the clock backwards.
		baseTicks := int64(c.ext)
		at := baseTicks + delta
		if at < 0 {
			return Rational{}, fmt.Errorf("%w: RTP timestamp rewind", ErrCorrupt)
		}
		return c.tickTime(uint64(at))
	}
	c.ext += uint64(delta)
	c.last = ts
	return c.tickTime(c.ext)
}

// Time converts ts without advancing the high-water mark.
func (c *RTPClock) Time(ts uint32) (Rational, error) {
	delta := int64(int32(ts - c.last))
	at := int64(c.ext) + delta
	if at < 0 {
		return Rational{}, fmt.Errorf("%w: RTP timestamp rewind", ErrCorrupt)
	}
	return c.tickTime(uint64(at))
}

func (c *RTPClock) tickTime(ticks uint64) (Rational, error) {
	offset, err := Make(int64(ticks), int64(c.rate))
	if err != nil {
		return Rational{}, err
	}
	return c.base.Add(offset)
}

// ParseRTPHeader decodes a raw RTP packet header with pion/rtp. It exists so
// callers can feed Advance/Time from recorded primary and backup streams.
func ParseRTPHeader(raw []byte) (*rtp.Header, error) {
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("%w: rtp header: %v", ErrCorrupt, err)
	}
	if p.Version != 2 {
		return nil, fmt.Errorf("%w: rtp version %d", ErrCorrupt, p.Version)
	}
	h := p.Header
	return &h, nil
}

// MarshalRTPHeader is the inverse of ParseRTPHeader, used by fixtures.
func MarshalRTPHeader(h *rtp.Header) ([]byte, error) {
	return h.Marshal()
}

// SwitchPlan is the outcome of planning a primary-to-backup transition.
type SwitchPlan struct {
	// Boundary is the programme time at which the incoming source starts.
	Boundary Rational
	// IncomingStart is the incoming source's own time for its first packet.
	IncomingStart Rational
	// Offset must be added to incoming timestamps to continue the programme.
	Offset Rational
	// Forward is non-zero when the backup is late and media would be missing;
	// Backward is non-zero when the backup repeats media already delivered.
	Forward  Rational
	Backward Rational
	// Discontinuous reports that the sources did not line up exactly.
	Discontinuous bool
}

// PlanSwitch models a source switchover. The outgoing source's last packet and
// the incoming source's first packet anchor the two clocks; the plan computes
// the offset that preserves programme continuity, plus any discontinuity the
// delivery must account for.
func PlanSwitch(out *RTPClock, outLast uint32, in *RTPClock, inFirst uint32, step Rational) (SwitchPlan, error) {
	if out == nil || in == nil {
		return SwitchPlan{}, fmt.Errorf("%w: nil RTP clock", ErrCorrupt)
	}
	if step.N == 0 || step.D == 0 {
		return SwitchPlan{}, fmt.Errorf("%w: zero switch step", ErrOverflow)
	}
	last, err := out.Advance(outLast)
	if err != nil {
		return SwitchPlan{}, err
	}
	boundary, err := last.Add(step)
	if err != nil {
		return SwitchPlan{}, err
	}
	start, err := in.Advance(inFirst)
	if err != nil {
		return SwitchPlan{}, err
	}

	plan := SwitchPlan{Boundary: boundary, IncomingStart: start}
	switch start.Cmp(boundary) {
	case 1:
		gap, err := start.Sub(boundary)
		if err != nil {
			return SwitchPlan{}, err
		}
		plan.Forward = gap
		plan.Discontinuous = true
		// Pull the backup back to the boundary.
		off, err := boundary.Sub(start)
		if err != nil {
			return SwitchPlan{}, err
		}
		plan.Offset = off
	case -1:
		overlap, err := boundary.Sub(start)
		if err != nil {
			return SwitchPlan{}, err
		}
		plan.Backward = overlap
		plan.Discontinuous = true
		off, err := boundary.Sub(start)
		if err != nil {
			return SwitchPlan{}, err
		}
		plan.Offset = off
	default:
		plan.Offset = Zero()
	}
	return plan, nil
}
