// Package manifest builds segmented stream manifests on the shared media
// timeline and validates time-range alignment and bitrate against target.
package manifest

import (
	"errors"
	"fmt"
	"math"

	"mediadag/internal/inspect"
)

// ErrTimeUncertain is returned when the timeline has no anchor and the
// manifest must be blocked downstream.
var ErrTimeUncertain = errors.New("manifest: media time uncertain (no RTCP anchor)")

// Segment is one manifest entry covering [StartMs, EndMs) on the shared
// media timeline.
type Segment struct {
	Index   int
	StartMs float64
	EndMs   float64
	Bytes   int
	Frames  int
	HasIDR  bool
}

// Report summarizes the manifest and its alignment to the ideal grid.
type Report struct {
	TargetKbps     float64
	ActualKbps     float64 // delivered bitrate, computed from received bytes
	SentKbps       float64 // estimated sender bitrate, grossed up by loss
	LossRatio      float64 // observed unique-packet loss in [0,1]
	AlignmentErrMs float64 // max |segment start - grid boundary|
	StartMs        float64
	EndMs          float64
	Segments       []Segment
}

// Build segments the analyzed stream into segmentMs-long entries aligned
// to the media-timeline grid anchored at the timeline origin (t=0).
// lossRatio is the fraction of media packets never observed; it is used to
// estimate the sender-side bitrate independently of transport loss.
func Build(an *inspect.Analysis, segmentMs, targetKbps, lossRatio float64) (*Report, error) {
	if an == nil || an.TimeUncertain {
		return nil, ErrTimeUncertain
	}
	if segmentMs <= 0 {
		return nil, fmt.Errorf("manifest: segment duration must be positive")
	}
	if len(an.FrameList) == 0 {
		return nil, fmt.Errorf("manifest: no frames to segment")
	}
	rep := &Report{TargetKbps: targetKbps}
	if lossRatio < 0 {
		lossRatio = 0
	}
	if lossRatio > 0.99 {
		lossRatio = 0.99
	}
	rep.LossRatio = lossRatio

	segIdx := func(t float64) int { return int(t / segmentMs) }
	first := an.FrameList[0].TimeMs
	last := an.FrameList[len(an.FrameList)-1].TimeMs
	rep.StartMs = first
	nSeg := segIdx(last) + 1
	rep.Segments = make([]Segment, nSeg)
	for i := range rep.Segments {
		rep.Segments[i] = Segment{
			Index:   i,
			StartMs: float64(i) * segmentMs,
			EndMs:   float64(i+1) * segmentMs,
		}
	}
	for _, f := range an.FrameList {
		s := &rep.Segments[segIdx(f.TimeMs)]
		s.Bytes += f.Bytes
		s.Frames++
		s.HasIDR = s.HasIDR || f.IDR
	}
	rep.EndMs = rep.Segments[nSeg-1].EndMs

	// alignment: deviation of each segment's first frame from the grid
	for i, s := range rep.Segments {
		first := math.Inf(1)
		for _, f := range an.FrameList {
			if segIdx(f.TimeMs) == i && f.TimeMs < first {
				first = f.TimeMs
			}
		}
		if math.IsInf(first, 1) {
			continue // empty segment (all frames lost)
		}
		if d := math.Abs(first - s.StartMs); d > rep.alignmentErr() {
			rep.AlignmentErrMs = d
		}
	}

	// actual bitrate over the manifest time range
	dur := (rep.EndMs - rep.StartMs) / 1000
	if dur > 0 {
		rep.ActualKbps = float64(an.MediaBytes) * 8 / dur / 1000
		// sender-side estimate: the bytes we did receive, grossed up by the
		// share of packets that never arrived. If loss accounts for the gap
		// to target, SentKbps stays near target and the deviation is
		// transport loss, not a real encode change.
		if lossRatio > 0 {
			rep.SentKbps = rep.ActualKbps / (1 - lossRatio)
		} else {
			rep.SentKbps = rep.ActualKbps
		}
	}
	return rep, nil
}

func (r *Report) alignmentErr() float64 { return r.AlignmentErrMs }

// BitrateDeviationPct returns |actual-target|/target in percent.
func (r *Report) BitrateDeviationPct() float64 {
	if r.TargetKbps <= 0 {
		return 0
	}
	return math.Abs(r.ActualKbps-r.TargetKbps) / r.TargetKbps * 100
}

// SentDeviationPct returns |sent-target|/target in percent, where sent is
// the loss-compensated sender estimate.
func (r *Report) SentDeviationPct() float64 {
	if r.TargetKbps <= 0 {
		return 0
	}
	return math.Abs(r.SentKbps-r.TargetKbps) / r.TargetKbps * 100
}

// DeviationCause explains whether bitrate deviation comes from transport
// loss or from an actual change in the sender's encoding.
func (r *Report) DeviationCause() string {
	switch {
	case r.LossRatio == 0:
		if r.SentDeviationPct() > 8 {
			return "sender-change"
		}
		return "none"
	case r.SentDeviationPct() <= 8 && r.BitrateDeviationPct() > 8:
		return "packet-loss"
	case r.SentDeviationPct() > 8:
		return "sender-change"
	default:
		return "none"
	}
}

// Summary renders a one-line digest.
func (r *Report) Summary() string {
	return fmt.Sprintf("segments=%d range=[%.0f,%.0f]ms target=%.0fkbps actual=%.0fkbps sent=%.0fkbps loss=%.2f%% cause=%s align=%.2fms",
		len(r.Segments), r.StartMs, r.EndMs, r.TargetKbps, r.ActualKbps, r.SentKbps,
		r.LossRatio*100, r.DeviationCause(), r.AlignmentErrMs)
}
