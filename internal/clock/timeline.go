package clock

import (
	"math"

	"github.com/pion/rtcp"
)

const (
	SequenceModulus  = 1 << 16
	TimestampModulus = 1 << 32
	halfSequence     = SequenceModulus / 2
	halfTimestamp    = TimestampModulus / 2
	ntpEpochOffset   = 2208988800 // seconds between 1900-01-01 and 1970-01-01
)

// ExpandSequence unwraps a 16-bit RTP sequence number against the previous
// expanded value. The first call initializes the range at zero.
func ExpandSequence(last uint64, hasLast bool, value uint16) uint64 {
	if !hasLast {
		return uint64(value)
	}
	lastLow := int64(last & 0xffff)
	delta := int64(value) - lastLow
	if delta > halfSequence {
		delta -= SequenceModulus
	} else if delta < -halfSequence {
		delta += SequenceModulus
	}
	return uint64(int64(last) + delta)
}

// ExpandTimestamp unwraps a 32-bit RTP timestamp using shortest signed wrap.
func ExpandTimestamp(last uint64, hasLast bool, value uint32) uint64 {
	if !hasLast {
		return uint64(value)
	}
	lastLow := int64(last & 0xffffffff)
	delta := int64(value) - lastLow
	if delta > halfTimestamp {
		delta -= TimestampModulus
	} else if delta < -halfTimestamp {
		delta += TimestampModulus
	}
	return uint64(int64(last) + delta)
}

// NTPToUnixMilli converts the 64-bit NTP fixed-point timestamp used by RTCP.
func NTPToUnixMilli(ntp uint64) int64 {
	seconds := int64(ntp>>32) - ntpEpochOffset
	fraction := ntp & 0xffffffff
	millis := int64((fraction*1000 + (1 << 31)) >> 32)
	return seconds*1000 + millis
}

// UnixMilliToNTP is primarily used by deterministic sample generators.
func UnixMilliToNTP(ms int64) uint64 {
	if ms < 0 {
		ms = 0
	}
	secs := ms / 1000
	rem := ms % 1000
	frac := uint64((rem << 32) / 1000)
	return uint64(secs+ntpEpochOffset)<<32 | frac
}

// Mapping is one validated RTCP Sender Report correspondence.
type Mapping struct {
	ExtRTPTimestamp uint64
	NTPUnixMs       int64
}

// Timeline owns the only media-time conversion used by the pipeline.
type Timeline struct {
	ClockRate uint32

	firstRTP     uint64
	hasFirstRTP  bool
	lastSequence uint64
	hasSequence  bool
	lastRTP      uint64
	hasRTP       bool

	originNTPMs int64
	hasOrigin   bool
	mapping     Mapping
	hasMapping  bool

	lastMediaMs int64
	hasMedia    bool

	AppliedReports int
	StaleReports   int
	BackwardClamps int
}

func NewTimeline(clockRate uint32) *Timeline {
	if clockRate == 0 {
		clockRate = 8000
	}
	return &Timeline{ClockRate: clockRate}
}

func (t *Timeline) ExpandSequence(value uint16) uint64 {
	next := ExpandSequence(t.lastSequence, t.hasSequence, value)
	t.lastSequence = next
	t.hasSequence = true
	return next
}

func (t *Timeline) ExpandTimestamp(value uint32) uint64 {
	next := ExpandTimestamp(t.lastRTP, t.hasRTP, value)
	t.lastRTP = next
	t.hasRTP = true
	if !t.hasFirstRTP {
		t.firstRTP = next
		t.hasFirstRTP = true
	}
	return next
}

// ObserveSenderReport installs a fresh mapping. Corrupt reports are rejected
// by the caller; a nil/error report should instead call MarkStaleReport.
func (t *Timeline) ObserveSenderReport(extRTPTimestamp uint64, report *rtcp.SenderReport) Mapping {
	if report == nil {
		t.MarkStaleReport()
		return t.mapping
	}
	m := Mapping{ExtRTPTimestamp: extRTPTimestamp, NTPUnixMs: NTPToUnixMilli(report.NTPTime)}
	if !t.hasOrigin {
		t.originNTPMs = m.NTPUnixMs
		t.hasOrigin = true
	}
	t.mapping = m
	t.hasMapping = true
	t.AppliedReports++
	return m
}

func (t *Timeline) MarkStaleReport() {
	t.StaleReports++
}

// MediaTime converts an expanded RTP timestamp to monotonic milliseconds from
// the first valid time origin. It never returns a value below the previous one.
func (t *Timeline) MediaTime(extRTPTimestamp uint64) int64 {
	var candidate int64
	switch {
	case t.hasMapping && t.hasOrigin:
		delta := int64(extRTPTimestamp) - int64(t.mapping.ExtRTPTimestamp)
		candidate = t.mapping.NTPUnixMs - t.originNTPMs +
			int64(math.Round(float64(delta)*1000/float64(t.ClockRate)))
	case t.hasFirstRTP:
		delta := int64(extRTPTimestamp) - int64(t.firstRTP)
		candidate = int64(math.Round(float64(delta) * 1000 / float64(t.ClockRate)))
	default:
		candidate = 0
	}

	if t.hasMedia && candidate < t.lastMediaMs {
		candidate = t.lastMediaMs
		t.BackwardClamps++
	}
	t.lastMediaMs = candidate
	t.hasMedia = true
	return candidate
}

// DurationForTimestamps returns rounded milliseconds for an RTP timestamp gap.
func (t *Timeline) DurationForTimestamps(fromExt, toExt uint64) int64 {
	if toExt <= fromExt {
		return 0
	}
	return int64(math.Round(float64(toExt-fromExt) * 1000 / float64(t.ClockRate)))
}
