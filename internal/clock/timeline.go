package clock

import "fmt"

// Nanoseconds is the common monotonic time base used by audio and video.
type Nanoseconds int64

// Base is the shared origin for all streams in one region-node session.
type Base struct {
	OriginNS Nanoseconds
}

// NewBase creates a shared time base at originNS.
func NewBase(originNS int64) *Base {
	return &Base{OriginNS: Nanoseconds(originNS)}
}

// Timeline maps one RTP timestamp clock to the shared nanosecond base.
//
// The zero value is not usable; call NewTimeline.
type Timeline struct {
	stream   StreamID
	ssrc     uint32
	rateHz   uint64
	originNS int64

	have        bool
	firstExt    int64
	maxExt      int64
	anchorExt   int64
	anchorNS    int64
	rtcpReports int
}

// NewTimeline constructs a mapping for rateHz (for example 48000 or 90000).
func NewTimeline(stream StreamID, ssrc, rateHz uint32, originNS int64) (*Timeline, error) {
	if rateHz == 0 {
		return nil, fmt.Errorf("clock: zero RTP rate for %s", stream)
	}
	return &Timeline{
		stream:   stream,
		ssrc:     ssrc,
		rateHz:   uint64(rateHz),
		originNS: originNS,
	}, nil
}

// Stream returns the stream identifier.
func (t *Timeline) Stream() StreamID { return t.stream }

// SSRC returns the RTP SSRC.
func (t *Timeline) SSRC() uint32 { return t.ssrc }

// RateHz returns the RTP timestamp clock rate.
func (t *Timeline) RateHz() uint64 { return t.rateHz }

// Reports returns the number of applied RTCP reports.
func (t *Timeline) Reports() int { return t.rtcpReports }

// Unwrap32 extends a 32-bit RTP timestamp around a known extended reference.
// It correctly advances through the 2^32 -> 0 wrap.
func Unwrap32(reference uint64, value uint32) int64 {
	delta := int32(value - uint32(reference))
	return int64(reference) + int64(delta)
}

// Observe records a timestamp and returns its unwrapped extended value.
func (t *Timeline) Observe(ts uint32) int64 {
	if !t.have {
		ext := int64(ts)
		t.have = true
		t.firstExt = ext
		t.maxExt = ext
		t.anchorExt = ext
		t.anchorNS = t.originNS
		return ext
	}
	ext := Unwrap32(uint64(t.maxExt), ts)
	if ext > t.maxExt {
		t.maxExt = ext
	}
	return ext
}

// Map converts an RTP timestamp to nanoseconds on the shared base.
func (t *Timeline) Map(ts uint32) int64 {
	ext := t.Observe(ts)
	return t.anchorNS + ((ext - t.anchorExt) * int64(1_000_000_000) / int64(t.rateHz))
}

// Duration converts a number of RTP clock ticks to nanoseconds.
func (t *Timeline) Duration(ticks uint32) int64 {
	return int64(ticks) * 1_000_000_000 / int64(t.rateHz)
}

// ApplyRTCP uses a sender report's RTP/NTP correspondence to correct the
// mapping. It returns the correction applied to the previous mapping.
func (t *Timeline) ApplyRTCP(report RTCPReport) (correctionNS int64, err error) {
	if report.SSRC != 0 && t.ssrc != 0 && report.SSRC != t.ssrc {
		return 0, MediaError{Code: CodeRTCPInvalid, Stream: t.stream, SSRC: report.SSRC, Message: "SSRC mismatch"}
	}
	ext := t.Observe(report.RTPTimestamp)
	before := t.anchorNS + ((ext - t.anchorExt) * int64(1_000_000_000) / int64(t.rateHz))
	t.anchorExt = ext
	t.anchorNS = report.NTPNS
	t.rtcpReports++
	return report.NTPNS - before, nil
}

// State is the checkpointable timeline mapping state.
type State struct {
	Have      bool
	RateHz    uint32
	FirstExt  int64
	MaxExt    int64
	AnchorExt int64
	AnchorNS  int64
}

// Export returns the timeline state for a checkpoint.
func (t *Timeline) Export() State {
	return State{
		Have: t.have, RateHz: uint32(t.rateHz), FirstExt: t.firstExt,
		MaxExt: t.maxExt, AnchorExt: t.anchorExt, AnchorNS: t.anchorNS,
	}
}

// Restore reloads a checkpointed timeline.
func (t *Timeline) Restore(st State) {
	t.have = st.Have
	if st.RateHz != 0 {
		t.rateHz = uint64(st.RateHz)
	}
	t.firstExt = st.FirstExt
	t.maxExt = st.MaxExt
	t.anchorExt = st.AnchorExt
	t.anchorNS = st.AnchorNS
}

// MaxMapped is useful in tests and checkpoint state.
func (t *Timeline) MaxMapped() int64 {
	if !t.have {
		return t.originNS
	}
	return t.anchorNS + ((t.maxExt - t.anchorExt) * int64(1_000_000_000) / int64(t.rateHz))
}
