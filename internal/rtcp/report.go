// Package rtcp maps RTP timestamps and RTCP sender reports onto the unified
// rational time base in internal/clock.
//
// A sender report binds one RTP timestamp (a tick count on the track clock)
// to an NTP wall-clock instant. Every later RTP timestamp is converted
// relative to that anchor. 32-bit timestamp wrap is unwrapped with the same
// shortest-jump rule as sequence numbers; a jump reaching half the 32-bit
// space cannot be decided uniquely and is recorded as a time-base fault.
package rtcp

import (
	"media-check/internal/clock"
	"media-check/internal/fault"
	"media-check/internal/fixture"
)

// Anchor is one resolved RTCP sender-report binding.
type Anchor struct {
	TrackID uint32
	SSRC    uint32
	// RTPTicks is the RTP timestamp at the NTP instant, unwrapped to 64 bit.
	RTPTicks int64
	// Wall is the NTP instant as an exact rational number of seconds.
	Wall clock.Rational
	// ClockRate is the track RTP clock rate in Hz.
	ClockRate int64
}

// TimeMapper converts per-track RTP ticks to the shared rational time base.
type TimeMapper struct {
	anchors  map[uint32]*Anchor
	lastTick map[uint32]int64
	faults   *fault.Model
}

// NewTimeMapper constructs an empty mapper bound to a shared fault model.
func NewTimeMapper(fm *fault.Model) *TimeMapper {
	return &TimeMapper{
		anchors:  make(map[uint32]*Anchor),
		lastTick: make(map[uint32]int64),
		faults:   fm,
	}
}

// AddReport ingests a fixture RTCP report and establishes the track anchor.
func (m *TimeMapper) AddReport(r fixture.RTCPReport) error {
	wall, err := ntpToRational(r.NTPSeconds, r.NTPFrac)
	if err != nil {
		return err
	}
	m.anchors[r.TrackID] = &Anchor{
		TrackID:   r.TrackID,
		SSRC:      r.SSRC,
		RTPTicks:  int64(r.RTPTime),
		Wall:      wall,
		ClockRate: int64(r.ClockRate),
	}
	m.lastTick[r.TrackID] = int64(r.RTPTime)
	return nil
}

// Map converts a raw 32-bit RTP timestamp for a track into unified time.
// The boolean reports whether the mapping is unambiguous; on ambiguity the
// returned time still uses the shortest-jump interpretation and a time-base
// fault is recorded, so callers can choose to reject the whole run.
func (m *TimeMapper) Map(trackID uint32, rtpTimestamp uint32) (clock.Rational, bool) {
	a, ok := m.anchors[trackID]
	if !ok {
		m.faults.Record(fault.Fault{
			Code:     fault.CodeTimebaseWrap,
			Severity: fault.SeverityFatal,
			TrackID:  trackID,
			Message:  "no RTCP anchor for track; RTP time cannot be mapped",
		})
		return clock.Zero(), false
	}
	prev := m.lastTick[trackID]
	unwrapped, ambiguous := unwrap32(prev, rtpTimestamp)
	m.lastTick[trackID] = unwrapped
	if ambiguous {
		m.faults.Record(fault.Fault{
			Code:     fault.CodeTimebaseWrap,
			Severity: fault.SeverityFatal,
			TrackID:  trackID,
			Message:  "RTP timestamp jump reaches half the 32-bit space; wrap cannot be decided uniquely",
		})
	}
	deltaTicks := unwrapped - a.RTPTicks
	offset, err := clock.FromTicks(deltaTicks, a.ClockRate)
	if err != nil {
		m.faults.Record(fault.Fault{
			Code:     fault.CodeTimebaseWrap,
			Severity: fault.SeverityFatal,
			TrackID:  trackID,
			Message:  err.Error(),
		})
		return clock.Zero(), false
	}
	return a.Wall.Add(offset), !ambiguous
}

// AnchorWall exposes a track's anchored wall-clock instant (for reports).
func (m *TimeMapper) AnchorWall(trackID uint32) (clock.Rational, bool) {
	a, ok := m.anchors[trackID]
	if !ok {
		return clock.Zero(), false
	}
	return a.Wall, true
}

// ntpToRational turns NTP seconds + 32-bit fraction into an exact rational:
// fraction/2^32 seconds is added without floating-point conversion.
func ntpToRational(secs, frac uint32) (clock.Rational, error) {
	whole, err := clock.New(int64(secs), 1)
	if err != nil {
		return clock.Rational{}, err
	}
	// 2^32 = 4294967296; normalize inside clock.New so the fraction reduces.
	part, err := clock.New(int64(frac), 1<<32)
	if err != nil {
		return clock.Rational{}, err
	}
	return whole.Add(part), nil
}

// unwrap32 extends a 32-bit RTP timestamp relative to the previous unwrapped
// value using the signed modular delta. It reports ambiguity when the delta
// reaches half the 32-bit space, where wrap vs. a huge jump is unknowable.
func unwrap32(prev int64, cur uint32) (int64, bool) {
	prev32 := uint32(prev)
	modDelta := uint32(uint64(cur) - uint64(prev32))
	// Exactly half the 32-bit space (delta == -2^31) is equidistant between
	// "one forward wrap" and "one backward jump" and cannot be decided.
	ambiguous := modDelta == 0x80000000
	delta := int32(modDelta) // signed: -2^31..2^31-1
	return prev + int64(delta), ambiguous
}
