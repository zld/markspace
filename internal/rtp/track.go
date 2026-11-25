package rtp

import (
	"errors"

	"mediacheck/internal/clock"
)

// ErrUnknownSSRC means an RTCP report named an SSRC the receiver has
// never seen on an RTP track. The report is ignored rather than trusted.
var ErrUnknownSSRC = errors.New("RTCP report for unknown SSRC")

// TrackRegistry maps each RTP SSRC to one unified timeline so RTCP
// reports and RTP packets share the same media clock.
type TrackRegistry struct {
	clockRate uint32
	tracks    map[uint32]*clock.Timeline
}

// NewTrackRegistry builds a registry. All synthetic tracks in this
// kernel share one clock rate; real deployments would key it by payload
// type, which the fixture generator never mixes per registry.
func NewTrackRegistry(clockRate uint32) *TrackRegistry {
	return &TrackRegistry{
		clockRate: clockRate,
		tracks:    make(map[uint32]*clock.Timeline),
	}
}

// Timeline returns the timeline for an SSRC, creating it on first use.
func (r *TrackRegistry) Timeline(ssrc uint32) *clock.Timeline {
	tl, ok := r.tracks[ssrc]
	if !ok {
		tl = clock.NewTimeline(r.clockRate)
		r.tracks[ssrc] = tl
	}
	return tl
}

// ApplySenderReport binds an SR to its SSRC's timeline. Reports whose
// SSRC was never observed are rejected with ErrUnknownSSRC.
func (r *TrackRegistry) ApplySenderReport(sr SenderReport) error {
	tl, ok := r.tracks[sr.SSRC]
	if !ok {
		return ErrUnknownSSRC
	}
	tl.BindAnchor(clock.SenderAnchor{
		RTPTimestamp: sr.RTPTimestamp,
		NTPSeconds:   sr.NTPSeconds,
	})
	return nil
}

// SSRCs enumerates the tracks seen so far in first-seen-independent
// sorted order for deterministic summaries.
func (r *TrackRegistry) SSRCs() []uint32 {
	out := make([]uint32, 0, len(r.tracks))
	for ssrc := range r.tracks {
		out = append(out, ssrc)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
