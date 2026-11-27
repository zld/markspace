// Package clock turns RTP media clocks into one unified nanosecond
// timeline. A TrackClock is anchored either by an RTCP sender report
// (NTP wallclock plus RTP timestamp pair) or, when no report exists, by
// the first valid RTP packet. The anchor records whether the mapping is
// exact or estimated so downstream stages can label provenance honestly.
package clock

import (
	"fmt"
	"math"
	"sort"

	"replay/internal/fault"
)

// Source describes how a track clock was anchored.
type Source string

const (
	// SourceRTCP means the anchor came from an RTCP sender report.
	SourceRTCP Source = "rtcp"
	// SourceFirstPacket means no report existed and the first valid RTP
	// packet served as the estimate anchor.
	SourceFirstPacket Source = "estimated-first-packet"
)

// ntpUnixOffset is the number of seconds between the NTP epoch (1900)
// and the Unix epoch (1970).
const ntpUnixOffset = 2_208_988_800

// Tick is a point on a track's own clock.
type Tick struct {
	// Timestamp is the raw 32-bit RTP timestamp.
	Timestamp uint32
	// Seq is the extended (unwrapped) sequence number used for stable
	// ordering of same-instant events.
	Seq uint64
}

// TrackClock maps one RTP track clock to nanoseconds.
type TrackClock struct {
	track     string
	clockRate uint32
	baseRTP   uint32
	baseNS    int64
	source    Source
	anchored  bool

	// highWater is the highest unwrapped RTP timestamp mapped so far.
	// Packets arriving below it are regressions, because the ingest
	// stage already restored sequence order.
	highWater    uint64
	highWaterSet bool
}

// New creates an unanchored clock for a track. clockRate is the RTP
// timestamp frequency in hertz (for example 48000 for Opus).
func New(track string, clockRate uint32) (*TrackClock, error) {
	if track == "" {
		return nil, fault.New(fault.KindArgument, "track clock requires a track ID").At("clock")
	}
	if clockRate == 0 {
		return nil, fault.New(fault.KindArgument, "clock rate must be positive").
			At("clock").WithTrack(track)
	}
	return &TrackClock{track: track, clockRate: clockRate}, nil
}

// Track returns the logical track identifier.
func (c *TrackClock) Track() string { return c.track }

// ClockRate returns the RTP timestamp frequency.
func (c *TrackClock) ClockRate() uint32 { return c.clockRate }

// Source reports the anchoring provenance, or "" before anchoring.
func (c *TrackClock) Source() Source { return c.source }

// Anchored reports whether a mapping exists.
func (c *TrackClock) Anchored() bool { return c.anchored }

// AnchorRTCP pins the clock using an RTCP sender report. rtpTime is the
// RTP timestamp that corresponds to wallNS (nanoseconds since the Unix
// epoch). Anchoring twice is rejected so that a later report cannot move
// the origin of an already published timeline.
func (c *TrackClock) AnchorRTCP(rtpTime uint32, wallNS int64) error {
	if wallNS < 0 {
		return fault.New(fault.KindClock, "negative wallclock anchor").
			At("clock").WithTrack(c.track)
	}
	if c.anchored {
		return fault.New(fault.KindClock, "clock already anchored").
			At("clock").WithTrack(c.track)
	}
	c.baseRTP = rtpTime
	c.baseNS = wallNS
	c.source = SourceRTCP
	c.anchored = true
	return nil
}

// AnchorFirstPacket pins the clock with the first valid RTP packet. The
// caller chooses the nanosecond origin (normally 0 for an offline run),
// so every later timestamp on this track is relative to that packet.
func (c *TrackClock) AnchorFirstPacket(rtpTime uint32, originNS int64) error {
	if c.anchored {
		return fault.New(fault.KindClock, "clock already anchored").
			At("clock").WithTrack(c.track)
	}
	c.baseRTP = rtpTime
	c.baseNS = originNS
	c.source = SourceFirstPacket
	c.anchored = true
	return nil
}

// TimelinePoint is one packet's position on the unified timeline.
type TimelinePoint struct {
	Track     string
	Timestamp uint32
	Extended  uint64
	Seq       uint64
	At        int64
	Estimated bool
	Source    Source
	Ticks     int64
}

// Map converts a raw RTP timestamp into nanoseconds. Timestamps are
// unwrapped relative to the highest value seen so far, so a single
// forward wrap is recognised. Because ingest has already restored
// sequence order, any timestamp below the high-water mark is a genuine
// regression and is rejected with the track and sequence number attached.
func (c *TrackClock) Map(p Tick) (TimelinePoint, error) {
	if !c.anchored {
		return TimelinePoint{}, fault.New(fault.KindClock, "clock is not anchored").
			At("clock").WithTrack(c.track).WithSeq(p.Seq)
	}

	reference := uint64(c.baseRTP)
	if c.highWaterSet {
		reference = c.highWater
	}
	ext := unwrap(reference, p.Timestamp)

	if c.highWaterSet && ext < c.highWater {
		return TimelinePoint{}, fault.New(fault.KindClock, "rtp timestamp regressed").
			At("clock").WithTrack(c.track).WithSeq(p.Seq)
	}

	ticks := int64(ext) - int64(c.baseRTP)
	at, err := c.offsetNanoseconds(ticks)
	if err != nil {
		fe, ok := fault.As(err)
		if !ok {
			fe = fault.New(fault.KindClock, "nanosecond conversion failed").At("clock").Wrap(err)
		}
		return TimelinePoint{}, fe.WithTrack(c.track).WithSeq(p.Seq)
	}
	if (at > 0 && c.baseNS > math.MaxInt64-at) || (at < 0 && c.baseNS < math.MinInt64-at) {
		return TimelinePoint{}, fault.New(fault.KindClock, "timeline nanosecond overflow").
			At("clock").WithTrack(c.track).WithSeq(p.Seq)
	}

	c.highWater = ext
	c.highWaterSet = true

	return TimelinePoint{
		Track:     c.track,
		Timestamp: p.Timestamp,
		Extended:  ext,
		Seq:       p.Seq,
		At:        c.baseNS + at,
		Estimated: c.source == SourceFirstPacket,
		Source:    c.source,
		Ticks:     ticks,
	}, nil
}

// offsetNanoseconds converts a signed tick delta to nanoseconds using
// integer math so results are reproducible across machines. It reports
// an error when the multiplication would overflow int64.
func (c *TrackClock) offsetNanoseconds(ticks int64) (int64, error) {
	const nanosPerSecond = int64(1_000_000_000)
	rate := int64(c.clockRate)

	// |ticks| * 1e9 must fit: bound the magnitude before multiplying.
	limit := math.MaxInt64 / nanosPerSecond
	if ticks > limit || ticks < -limit {
		return 0, fault.New(fault.KindClock, "rtp tick delta overflows nanoseconds").At("clock")
	}
	scaled := ticks * nanosPerSecond
	// Integer division truncates toward zero, which is symmetric for a
	// signed delta and keeps forward/backward offsets mirror images.
	return scaled / rate, nil
}

// unwrap extends ts relative to reference. Forward motion of more than
// half the 32-bit space is read as a backward jump (an old reordered
// packet), and backward motion of more than half the space as a forward
// wrap.
func unwrap(reference uint64, ts uint32) uint64 {
	curLow := uint32(reference)
	diff := int64(int32(ts - curLow))
	return uint64(int64(reference) + diff)
}

// NTPToNanoseconds converts a 64-bit NTP timestamp (32.32 fixed point)
// into nanoseconds since the Unix epoch. It rejects timestamps whose
// second field would overflow int64 nanoseconds.
func NTPToNanoseconds(ntp uint64) (int64, error) {
	seconds := int64(ntp >> 32)
	fraction := int64(ntp & 0xffffffff)
	if seconds-ntpUnixOffset > math.MaxInt64/1_000_000_000 {
		return 0, fault.New(fault.KindClock, "ntp seconds overflow nanoseconds").At("clock")
	}
	secNS := (seconds - ntpUnixOffset) * 1_000_000_000
	// fraction / 2^32 seconds, expressed in nanoseconds.
	fracNS := (fraction * 1_000_000_000) >> 32
	return secNS + fracNS, nil
}

// SortPoints orders timeline points by instant, then track ID, then
// sequence number. This is the documented deterministic order for
// same-instant events.
func SortPoints(points []TimelinePoint) {
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].At != points[j].At {
			return points[i].At < points[j].At
		}
		if points[i].Track != points[j].Track {
			return points[i].Track < points[j].Track
		}
		return points[i].Seq < points[j].Seq
	})
}

// Describe renders a compact provenance summary for terminal output.
func (c *TrackClock) Describe() string {
	if !c.anchored {
		return fmt.Sprintf("%s rate=%d unanchored", c.track, c.clockRate)
	}
	return fmt.Sprintf("%s rate=%d source=%s base_ns=%d wrap=%d",
		c.track, c.clockRate, c.source, c.baseNS, c.highWater>>32)
}
