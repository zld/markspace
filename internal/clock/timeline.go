// Package clock provides a shared media time base across RTP tracks.
//
// RTP timestamps are 32-bit counters with independent clock rates and
// arbitrary starting offsets. RTCP Sender Reports tie an RTP timestamp to an
// absolute NTP instant, which lets the pipeline place every track on one
// millisecond timeline. A track whose sender reports are missing, reused, or
// non-monotonic gets an explicit untrusted interval: its times are marked as
// projections instead of being silently reset to zero.
package clock

import (
	"fmt"

	"mediatool/internal/fault"
)

// NTP is a 64-bit RFC 5905 timestamp: seconds since 1900 in the high word and
// a 32-bit fraction in the low word.
type NTP uint64

// FractionDenominator is the NTP fractional unit (2^32).
const FractionDenominator = 1 << 32

// NTPToNs converts an NTP timestamp to nanoseconds since the NTP epoch.
// The NTP epoch is far outside the Unix range, but the value still fits in an
// int64 (about 3.9e18 ns), so differences stay exact for elapsed-time use.
func NTPToNs(n NTP) int64 {
	sec := int64(n >> 32)
	frac := int64(n & 0xffffffff)
	return sec*1e9 + frac*1e9/FractionDenominator
}

// NsToNTP converts nanoseconds since the NTP epoch back to an NTP timestamp.
func NsToNTP(ns int64) NTP {
	if ns < 0 {
		ns = 0
	}
	sec := ns / 1e9
	rem := ns % 1e9
	return NTP(uint64(sec)<<32 | uint64(rem*FractionDenominator/1e9))
}

// SenderReport is the subset of an RTCP SR that the timeline needs.
type SenderReport struct {
	TrackID   uint32
	SSRC      uint32
	NTP       NTP
	RTP       uint32
	ClockRate uint32
	// Offset is the first byte of the RTCP packet in the input stream.
	Offset int64
	// PacketCount and OctetCount are carried through for diagnostics.
	PacketCount uint32
	OctetCount  uint32
}

// Validate rejects reports that cannot be placed on a timeline.
func (r SenderReport) Validate() error {
	if r.ClockRate == 0 {
		return (&fault.Fault{Kind: fault.KindRTCPInvalid, TrackID: r.TrackID, Offset: r.Offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("sender report for ssrc=%d has zero clock rate", r.SSRC)
	}
	return nil
}

// Untrusted is a half-open NTP interval in which the track clock cannot be
// relied upon. Media times inside it are projections from the last trusted
// sender report, never zero.
type Untrusted struct {
	TrackID uint32 `json:"track_id"`
	FromNs  int64  `json:"from_ns"`
	ToNs    int64  `json:"to_ns"`
	Reason  string `json:"reason"`
	Offset  int64  `json:"offset"`
}

// Stamped is the result of mapping one RTP timestamp onto the shared timeline.
type Stamped struct {
	// Valid is false when no trusted sender report exists yet.
	Valid bool
	// TrackID identifies the clock that produced the stamp.
	TrackID uint32
	// Extended is the wrap-extended RTP timestamp.
	Extended uint64
	// MediaMs is the media time relative to the first trusted sender report.
	MediaMs int64
	// NTPNs is the projected absolute NTP instant in nanoseconds.
	NTPNs int64
	// Trusted is false when NTPNs falls in an untrusted interval.
	Trusted bool
	// Reason explains why the stamp is untrusted or invalid.
	Reason string
}

// maxUntrusted bounds memory held per track regardless of stream length.
const maxUntrusted = 64

// TrackClock maps one RTP timestamp domain (one SSRC) onto media time.
type TrackClock struct {
	trackID   uint32
	ssrc      uint32
	clockRate uint32

	// Reordering-safe wrap extension: every value is derived from a nearby
	// reference point so a late packet cannot shift the epoch.
	haveRef bool
	refRaw  uint32
	refExt  uint64

	haveBase   bool
	baseNTPNs  int64
	baseExtRTP uint64

	lastSRNTPNs  int64
	lastSRRTPExt uint64
	lastSROffset int64
	srCount      int

	untrusted []Untrusted
	// open is set when a regression is detected and stays open until a
	// monotonic report proves the clock recovered. Timestamps after the
	// regression are therefore untrusted rather than silently accepted.
	open *Untrusted
}

// NewTrackClock creates a clock for a track. clockRate must be positive.
func NewTrackClock(trackID, ssrc, clockRate uint32) *TrackClock {
	return &TrackClock{trackID: trackID, ssrc: ssrc, clockRate: clockRate}
}

// TrackID returns the track this clock belongs to.
func (c *TrackClock) TrackID() uint32 { return c.trackID }

// SSRC returns the RTP synchronization source this clock tracks.
func (c *TrackClock) SSRC() uint32 { return c.ssrc }

// ClockRate returns the RTP ticks per second for this track.
func (c *TrackClock) ClockRate() uint32 { return c.clockRate }

// SRCount returns how many sender reports have been observed.
func (c *TrackClock) SRCount() int { return c.srCount }

// ExtendRTP expands a 32-bit RTP timestamp using signed modular distance from
// a moving reference. Reordered packets inside half the timestamp space map to
// the correct epoch; when the reference drifts more than a quarter space it is
// advanced, keeping the extension O(1) in memory.
func (c *TrackClock) ExtendRTP(ts uint32) uint64 {
	if !c.haveRef {
		c.haveRef = true
		c.refRaw = ts
		c.refExt = uint64(ts)
		return c.refExt
	}
	delta := int64(int32(ts - c.refRaw))
	ext := int64(c.refExt) + delta
	// Advance the reference when it lags or leads too far, so old and new
	// packets both stay within a representable signed window.
	if delta > 1<<30 || delta < -(1<<30) {
		c.refRaw = ts
		c.refExt = uint64(ext)
	}
	return uint64(ext)
}

// ObserveSR folds a sender report into the clock.
//
// A non-increasing NTP time, or an RTP time that moves backwards while NTP
// advances, marks the span since the last trusted report as untrusted and
// returns a located fault. The existing trusted base is retained so later
// timestamps keep a sane projection rather than restarting at zero.
func (c *TrackClock) ObserveSR(r SenderReport) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if c.clockRate == 0 {
		c.clockRate = r.ClockRate
	} else if r.ClockRate != c.clockRate {
		return (&fault.Fault{Kind: fault.KindRTCPInvalid, TrackID: c.trackID, Offset: r.Offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("sender report clock rate %d disagrees with track rate %d", r.ClockRate, c.clockRate)
	}
	ns := NTPToNs(r.NTP)
	ext := c.ExtendRTP(r.RTP)
	c.srCount++

	if !c.haveBase {
		c.haveBase = true
		c.baseNTPNs = ns
		c.baseExtRTP = ext
		c.lastSRNTPNs = ns
		c.lastSRRTPExt = ext
		c.lastSROffset = r.Offset
		return nil
	}

	// A report that is still non-monotonic against the last trusted one keeps
	// the untrusted interval open and extends it.
	var reason string
	switch {
	case ns <= c.lastSRNTPNs:
		reason = "rtcp_time_regression"
	case ext < c.lastSRRTPExt:
		reason = "rtcp_rtp_regression"
	}
	if reason != "" {
		from, to := ns, c.lastSRNTPNs
		if from > to {
			from, to = to, from
		}
		if c.open != nil {
			if from < c.open.FromNs {
				c.open.FromNs = from
			}
			if to > c.open.ToNs {
				c.open.ToNs = to
			}
			c.open.Offset = r.Offset
		} else {
			c.addUntrusted(Untrusted{TrackID: c.trackID, FromNs: from, ToNs: to, Reason: reason, Offset: r.Offset})
			c.open = &c.untrusted[len(c.untrusted)-1]
		}
		return (&fault.Fault{Kind: fault.KindRTCPInvalid, TrackID: c.trackID, Offset: r.Offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("%s: ntp %d -> %d rtp %d -> %d", reason, c.lastSRNTPNs, ns, c.lastSRRTPExt, ext)
	}

	// A monotonic report closes any open interval. Media time continues from
	// the trusted base and is not rebased, so the projection stays continuous.
	if c.open != nil {
		if ns > c.open.ToNs {
			c.open.ToNs = ns
		}
		c.open = nil
	}
	c.lastSRNTPNs = ns
	c.lastSRRTPExt = ext
	c.lastSROffset = r.Offset
	return nil
}

func (c *TrackClock) addUntrusted(u Untrusted) {
	c.untrusted = append(c.untrusted, u)
	if len(c.untrusted) > maxUntrusted {
		c.untrusted = c.untrusted[len(c.untrusted)-maxUntrusted:]
	}
}

// Untrusted returns a copy of the untrusted intervals recorded so far.
func (c *TrackClock) Untrusted() []Untrusted {
	out := make([]Untrusted, len(c.untrusted))
	copy(out, c.untrusted)
	return out
}

func (c *TrackClock) trustReason(ntpNs int64) (bool, string) {
	if c.open != nil && ntpNs >= c.open.FromNs {
		return false, fmt.Sprintf("%s at offset=%d (open)", c.open.Reason, c.open.Offset)
	}
	for i := len(c.untrusted) - 1; i >= 0; i-- {
		u := c.untrusted[i]
		if ntpNs >= u.FromNs && ntpNs <= u.ToNs {
			return false, fmt.Sprintf("%s at offset=%d", u.Reason, u.Offset)
		}
	}
	return true, ""
}

func ticksToNs(ticks int64, rate uint32) int64 {
	if rate == 0 {
		return 0
	}
	q := ticks / int64(rate)
	r := ticks % int64(rate)
	return q*1e9 + r*1e9/int64(rate)
}

// Map projects an RTP timestamp onto media time. It never fabricates a zero
// time for a missing report: Valid stays false and Reason is filled in.
func (c *TrackClock) Map(ts uint32) Stamped {
	ext := c.ExtendRTP(ts)
	st := Stamped{TrackID: c.trackID, Extended: ext}
	if !c.haveBase {
		st.Reason = "no trusted sender report before packet"
		return st
	}
	st.Valid = true
	delta := int64(ext) - int64(c.baseExtRTP)
	elapsed := ticksToNs(delta, c.clockRate)
	st.MediaMs = elapsed / 1e6
	st.NTPNs = c.baseNTPNs + elapsed
	st.Trusted, st.Reason = c.trustReason(st.NTPNs)
	if !st.Trusted {
		st.Reason = "untrusted interval: " + st.Reason
	}
	return st
}

// MapAt projects a timestamp and turns an unusable projection into a located
// fault. offset is the byte position of the packet that carried the timestamp.
func (c *TrackClock) MapAt(ts uint32, offset int64) (Stamped, error) {
	st := c.Map(ts)
	if !st.Valid {
		return st, (&fault.Fault{Kind: fault.KindRTCPInvalid, TrackID: c.trackID, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("%s (rtp=%d)", st.Reason, ts)
	}
	if !st.Trusted {
		return st, (&fault.Fault{Kind: fault.KindRTCPInvalid, TrackID: c.trackID, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("%s (media_ms=%d)", st.Reason, st.MediaMs)
	}
	return st, nil
}

// Timeline is the set of per-track clocks that share one media epoch.
// The epoch is the NTP instant of the first trusted sender report on any
// track; every track's MediaMs is measured from its own first report, so
// tracks agree once their reports agree.
type Timeline struct {
	clocks map[uint32]*TrackClock
	ssrcs  map[uint32]*TrackClock
}

// New creates an empty timeline.
func New() *Timeline {
	return &Timeline{clocks: make(map[uint32]*TrackClock), ssrcs: make(map[uint32]*TrackClock)}
}

// Register adds or replaces the clock for a track.
func (t *Timeline) Register(trackID, ssrc, clockRate uint32) *TrackClock {
	c := NewTrackClock(trackID, ssrc, clockRate)
	t.clocks[trackID] = c
	t.ssrcs[ssrc] = c
	return c
}

// Clock returns the clock registered for a track.
func (t *Timeline) Clock(trackID uint32) (*TrackClock, bool) {
	c, ok := t.clocks[trackID]
	return c, ok
}

// ObserveSR routes a report to its track clock. Unknown SSRCs are registered
// using the report's TrackID so a stream can start with RTCP.
func (t *Timeline) ObserveSR(r SenderReport) error {
	c, ok := t.ssrcs[r.SSRC]
	if !ok {
		if existing, ok2 := t.clocks[r.TrackID]; ok2 && r.TrackID != 0 {
			c = existing
			c.ssrc = r.SSRC
			t.ssrcs[r.SSRC] = c
		} else {
			c = t.Register(r.TrackID, r.SSRC, r.ClockRate)
		}
	}
	return c.ObserveSR(r)
}

// Untrusted returns every recorded untrusted interval, ordered by track.
func (t *Timeline) Untrusted() []Untrusted {
	out := make([]Untrusted, 0, len(t.clocks))
	for _, c := range t.clocks {
		out = append(out, c.Untrusted()...)
	}
	return out
}
