// Package fanout fans one generated source out into independent per-rendition
// recovery pipelines and aligns their random-access boundaries on the shared
// media time base.
package fanout

import (
	"mediasync/internal/clock"
	"mediasync/internal/fixture"
	"mediasync/internal/repair"
	msrtp "mediasync/internal/rtp"
)

// AlignmentThresholdNS bounds how far two renditions' keyframes may diverge
// before the shared switch point is marked unavailable. One 30 fps frame is
// ~33.3 ms; manifest acceptance is stricter than a full frame period.
const AlignmentThresholdNS int64 = 8_000_000

// SwitchPoint is one shared random-access position across renditions.
type SwitchPoint struct {
	Index         int
	MediaNS       int64
	PerRendition  []int // sample index per rendition, -1 when unavailable
	MaxSkewNS     int64
	Available     bool
	RejectedCause clock.ErrorKind
}

// RenditionResult holds one layer's recovered timeline and counters.
type RenditionResult struct {
	Rendition int
	SSRC      uint32
	Bitrate   int
	Samples   []repair.Sample
	Keyframes []repair.Sample
	Counters  repair.TimelineCounters
	Reorder   msrtp.ReorderCounters
	DriftPPM  float64
	Wraps     int
	Ingestor  *msrtp.Ingestor
}

// Ladder runs the recovery fan-out and boundary alignment.
type Ladder struct {
	streams []RenditionResult
	points  []SwitchPoint
}

// Recover ingests every rendition independently, fitting each device clock
// from its own RTCP sender reports before rebuilding the media timeline.
func Recover(stream *fixture.Stream) (*Ladder, *clock.Error) {
	l := &Ladder{}
	for rend, rs := range stream.Renditions {
		res, err := recoverRendition(stream, rend, rs)
		if err != nil {
			return nil, err
		}
		l.streams = append(l.streams, res)
	}
	l.align()
	return l, nil
}

func recoverRendition(stream *fixture.Stream, rend int, rs fixture.RenditionStream) (RenditionResult, *clock.Error) {
	cfg := stream.Cfg
	ing, err := msrtp.NewIngestor(rs.SSRC, cfg.ClockRate, cfg.ReorderWindow)
	if err != nil {
		return RenditionResult{}, err
	}
	truncatedSeqs := map[uint16]bool{}
	for _, dg := range rs.Datagrams {
		if dg.Truncated {
			truncatedSeqs[dg.Seq] = true
		}
	}
	// Ingest every datagram, draining released frames as the reorder window
	// commits them. Frames released mid-stream must be consumed immediately;
	// waiting until end of input would miss packets already emitted.
	var frames []msrtp.Frame
	for _, dg := range rs.Datagrams {
		if _, ierr := ing.IngestRaw(dg.Data); ierr != nil {
			// A datagram that cannot be parsed at all is counted via reorder
			// metrics; structural damage is judged again after indexing.
			continue
		}
		released, derr := ing.Ordered()
		if derr != nil {
			return RenditionResult{}, derr
		}
		frames = append(frames, released...)
	}
	tail, ferr := ing.Finalize()
	if ferr != nil {
		return RenditionResult{}, ferr
	}
	frames = append(frames, tail...)
	if len(frames) == 0 {
		return RenditionResult{}, clock.NewError(clock.ErrUnrecoverable, "rendition produced no decodable frames")
	}
	// Shared origin: every SR raw timestamp is unwrapped against the first
	// frame's raw value so estimator and frame timeline use one RTP base.
	origin := frames[0].Packet.Timestamp
	est := clock.NewDriftEstimator()
	for _, bundle := range rs.Reports {
		sr, perr := msrtp.ParseSenderReport(bundle.Data, bundle.ArrivalNS)
		if perr != nil {
			return RenditionResult{}, perr
		}
		ext, _, uerr := clock.UnwrapTS(int64(origin), sr.RTPTime)
		if uerr != nil {
			return RenditionResult{}, uerr
		}
		if e := est.Add(clock.SRPoint{RTP: ext, WallNS: sr.WallNS(), Arrival: sr.ArrivalNS}); e != nil {
			return RenditionResult{}, e
		}
	}
	builder := repair.NewTimelineBuilder(rs.SSRC, cfg.ClockRate, clock.MaxPacketBytes)
	builder.FitClock(est)
	for _, f := range frames {
		key := isGroundKeyframe(stream.Ground[rend], f.Packet.SequenceNumber)
		builder.AddFrame(f, key, truncatedSeqs[f.Packet.SequenceNumber])
	}
	samples := builder.Samples()
	kfs := make([]repair.Sample, 0, len(samples)/cfg.GOPFrames+1)
	for _, s := range samples {
		if s.IsKeyframe && !s.Isolated {
			kfs = append(kfs, s)
		}
	}
	return RenditionResult{
		Rendition: rend,
		SSRC:      rs.SSRC,
		Bitrate:   rs.Bitrate,
		Samples:   samples,
		Keyframes: kfs,
		Counters:  builder.Counters(),
		Reorder:   ing.Counters(),
		DriftPPM:  builder.DriftPPM(),
		Wraps:     ing.Wraps(),
		Ingestor:  ing,
	}, nil
}

// align pairs keyframes across renditions by nearest media time and marks any
// switch point whose skew exceeds the threshold as unavailable.
func (l *Ladder) align() {
	if len(l.streams) == 0 {
		return
	}
	base := l.streams[0].Keyframes
	for ki, kf := range base {
		sp := SwitchPoint{Index: ki, MediaNS: kf.MediaNS, PerRendition: make([]int, len(l.streams)), MaxSkewNS: 0}
		for i := range sp.PerRendition {
			sp.PerRendition[i] = -1
		}
		sp.PerRendition[0] = kf.Index
		for rend := 1; rend < len(l.streams); rend++ {
			idx, skew := nearestKeyframe(l.streams[rend].Keyframes, kf.MediaNS)
			if idx < 0 {
				sp.Available = false
				sp.RejectedCause = clock.ErrMissingKeyframe
				continue
			}
			sp.PerRendition[rend] = idx
			if skew > sp.MaxSkewNS {
				sp.MaxSkewNS = skew
			}
		}
		if sp.MaxSkewNS > AlignmentThresholdNS {
			sp.Available = false
			sp.RejectedCause = clock.ErrMissingKeyframe
		} else if sp.RejectedCause == clock.ErrNone {
			sp.Available = true
		}
		l.points = append(l.points, sp)
	}
}

// nearestKeyframe returns the sample index and absolute skew of the keyframe
// closest in media time; -1 when the layer has no keyframes.
func nearestKeyframe(kfs []repair.Sample, targetNS int64) (int, int64) {
	if len(kfs) == 0 {
		return -1, 0
	}
	best := 0
	bestSkew := abs64(kfs[0].MediaNS - targetNS)
	for i := 1; i < len(kfs); i++ {
		skew := abs64(kfs[i].MediaNS - targetNS)
		if skew < bestSkew {
			best, bestSkew = i, skew
		}
	}
	return kfs[best].Index, bestSkew
}

func isGroundKeyframe(ground []fixture.GroundFrame, seq uint16) bool {
	if int(seq) >= len(ground) {
		return false
	}
	return ground[seq].IsKeyframe
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Streams exposes per-rendition recovery results.
func (l *Ladder) Streams() []RenditionResult { return l.streams }

// SwitchPoints exposes the aligned boundary decisions.
func (l *Ladder) SwitchPoints() []SwitchPoint { return l.points }

// RejectedBoundaries counts switch points marked unavailable.
func (l *Ladder) RejectedBoundaries() int {
	n := 0
	for _, p := range l.points {
		if !p.Available {
			n++
		}
	}
	return n
}
