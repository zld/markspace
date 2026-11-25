// Package clock owns every time representation in the pipeline. RTP
// sequence numbers and timestamps arrive as 16/32 bit wrapping counters;
// the rest of the kernel only sees fully expanded int64 values on a
// single media clock. RTCP sender reports attach a wall-clock anchor to
// that clock without replacing the original RTP timestamps.
package clock

import "errors"

// Wrap16 is the modulus of an RTP sequence-number counter.
const Wrap16 = 1 << 16

// Wrap32 is the modulus of an RTP timestamp counter.
const Wrap32 = 1 << 32

// delta32 interprets the difference from prev to next as a signed 32-bit
// step, which is exactly the wraparound rule from RFC 3550 appendix A.
func delta32(prev, next uint32) int64 {
	return int64(int32(next - prev))
}

// Expander turns one wrapping counter into a monotone int64 stream.
type Expander struct {
	last   uint64
	raw    uint32
	seeded bool
}

// Feed maps a raw 32-bit counter value to its expanded value. The first
// observation defines the origin (expanded value equals the raw value);
// later values may move backwards by at least half the counter space and
// still be interpreted as forward wraparound.
func (e *Expander) Feed(v uint32) uint64 {
	if !e.seeded {
		e.seeded = true
		e.raw = v
		e.last = uint64(v)
		return e.last
	}
	e.last = uint64(int64(e.last) + delta32(e.raw, v))
	e.raw = v
	return e.last
}

// Value returns the most recent expanded value, or 0 before Feed.
func (e *Expander) Value() uint64 { return e.last }

// Seeded reports whether Feed has been called at least once.
func (e *Expander) Seeded() bool { return e.seeded }

// SenderAnchor pairs an RTP timestamp with the wall-clock instant named
// by an RTCP sender report (NTP seconds since 1900, as a float).
type SenderAnchor struct {
	RTPTimestamp uint32
	NTPSeconds   float64
}

// ErrNoAnchor is returned when wall-clock conversion is requested before
// any RTCP sender report has been observed.
var ErrNoAnchor = errors.New("no RTCP sender report anchor available")

// Timeline is the per-track unified clock. It expands timestamps, keeps
// the raw value for evidence and optionally anchors the media clock to
// the wall clock advertised by RTCP.
type Timeline struct {
	exp        Expander
	clockRate  uint32
	anchor     *SenderAnchor
	anchorExp  uint64
	trusted    bool
	lastRaw    uint32
	hasLastRaw bool
}

// NewTimeline builds a timeline for an RTP clock running at clockRate
// hertz. Time confidence starts low and becomes trusted once an RTCP
// sender report is associated with BindAnchor.
func NewTimeline(clockRate uint32) *Timeline {
	return &Timeline{clockRate: clockRate}
}

// Feed expands one raw RTP timestamp onto the monotone media clock.
// Repeated equal timestamps (the packets of one video frame) collapse to
// the same expanded value, as required for per-sample timing.
func (t *Timeline) Feed(raw uint32) int64 {
	t.lastRaw = raw
	t.hasLastRaw = true
	return int64(t.exp.Feed(raw))
}

// BindAnchor records an RTCP sender-report anchor and marks the timeline
// wall-clock trustworthy. The anchor's RTP timestamp is expanded through
// the same state machine so it shares the media origin.
func (t *Timeline) BindAnchor(a SenderAnchor) {
	expanded := t.exp.Feed(a.RTPTimestamp)
	t.anchor = &a
	t.anchorExp = expanded
	t.trusted = true
}

// Trusted reports whether wall-clock conversion is supported. A missing
// RTCP report keeps the timeline usable in media ticks but untrusted.
func (t *Timeline) Trusted() bool { return t.trusted }

// ClockRate returns the RTP clock rate in hertz.
func (t *Timeline) ClockRate() uint32 { return t.clockRate }

// ToWall converts an expanded media-tick value to NTP seconds. It returns
// ErrNoAnchor when no sender report has been seen.
func (t *Timeline) ToWall(expanded int64) (float64, error) {
	if t.anchor == nil || t.clockRate == 0 {
		return 0, ErrNoAnchor
	}
	deltaTicks := float64(expanded - int64(t.anchorExp))
	return t.anchor.NTPSeconds + deltaTicks/float64(t.clockRate), nil
}

// OriginalTS returns the last raw RTP timestamp fed, for evidence.
func (t *Timeline) OriginalTS() (uint32, bool) {
	return t.lastRaw, t.hasLastRaw
}
