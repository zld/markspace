// Package rtcp maps RTP timestamps to the unified media timeline using
// sender-report anchors (NTP wall clock + RTP timestamp) per track/SSRC.
package rtcp

import (
	"fmt"

	"mediarebuild/internal/model"
)

// rtpHalfRange is half the 32-bit RTP clock range. A jump of this size or
// larger between consecutive timestamps cannot be attributed to a single
// wrap direction and is reported as ambiguous.
const rtpHalfRange = uint64(1) << 31
const rtpFullRange = uint64(1) << 32

// anchor binds one RTP timestamp to a unified-timeline position and
// tracks the highest extended timestamp mapped so far.
type anchor struct {
	rtpBase uint32
	mediaAt model.MediaTime
	lastExt uint64 // extended RTP clock, initialised to rtpBase
}

// Mapper holds per-track (SSRC) anchors and converts RTP timestamps to
// the unified timeline.
type Mapper struct {
	tracks map[uint32]*anchor
}

// NewMapper creates an empty mapper.
func NewMapper() *Mapper { return &Mapper{tracks: make(map[uint32]*anchor)} }

// AddSenderReport records an anchor: at wall-clock ntpNanos the track's
// RTP clock read rtpTS.
func (m *Mapper) AddSenderReport(ssrc uint32, ntpNanos int64, rtpTS uint32) {
	m.tracks[ssrc] = &anchor{
		rtpBase: rtpTS,
		mediaAt: model.MediaTime(ntpNanos),
		lastExt: uint64(rtpTS),
	}
}

// Map converts an RTP timestamp for ssrc to the unified timeline.
// Missing anchors, wrap ambiguity and overflow produce structured errors.
func (m *Mapper) Map(ssrc uint32, timescale int64, rtpTS uint32) (model.MediaTime, error) {
	a, ok := m.tracks[ssrc]
	if !ok {
		return 0, model.NewDiag(model.StageRTCP, "no-mapping", model.NoOffset, false,
			fmt.Sprintf("no sender-report anchor for ssrc %d", ssrc))
	}
	tb := model.Timescale(timescale)
	if !tb.Valid() {
		return 0, model.NewDiag(model.StageRTCP, "bad-timescale", model.NoOffset, false,
			fmt.Sprintf("invalid timescale %d for ssrc %d", timescale, ssrc))
	}
	ext := extend(rtpTS, a.lastExt)
	if d := delta(ext, a.lastExt); d >= rtpHalfRange {
		return 0, model.NewDiag(model.StageRTCP, "ambiguous-wrap", model.NoOffset, false,
			fmt.Sprintf("ssrc %d: rtp jump of %d ticks exceeds half the clock range", ssrc, d))
	}
	var d64 int64
	if ext >= uint64(a.rtpBase) {
		d64 = int64(ext - uint64(a.rtpBase))
	} else {
		d64 = -int64(uint64(a.rtpBase) - ext)
	}
	d, err := tb.ToMedia(d64)
	if err != nil {
		return 0, model.NewDiag(model.StageRTCP, "overflow", model.NoOffset, false,
			fmt.Sprintf("ssrc %d: %v", ssrc, err))
	}
	if ext > a.lastExt {
		a.lastExt = ext
	}
	return a.mediaAt + d, nil
}

// extend lifts a 32-bit RTP timestamp into 64 bits closest to ref,
// assuming at most one wrap between consecutive observations.
func extend(seq uint32, ref uint64) uint64 {
	cand := (ref &^ 0xFFFFFFFF) | uint64(seq)
	if cand+rtpHalfRange < ref {
		cand += rtpFullRange
	} else if ref+rtpHalfRange < cand && cand >= rtpFullRange {
		cand -= rtpFullRange
	}
	return cand
}

func delta(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
