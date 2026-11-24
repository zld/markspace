package pipeline

import (
	"math"

	"mediascan/internal/clock"
	"mediascan/internal/index"
	"mediascan/internal/report"
	"mediascan/internal/stream"
)

// TransformState threads the running time line and keyframe cursor across
// segment boundaries, which keeps VFR keyframe intervals contiguous.
type TransformState struct {
	GlobalTicks   int64
	GlobalSeconds float64
	PrevKeyTime   float64
	HavePrevKey   bool
}

// TransformStage maps parsed samples onto the unified rational second
// time base with the configured drift correction, builds keyframe index
// entries and quarantines anything that depends on a damaged region.
func (p *Pipeline) TransformStage(ps *ParsedSegment, st *TransformState) (*NormalizedSegment, *TransformState) {
	in := ps.Input
	ns := &NormalizedSegment{
		Segment: in.Name, MediaSeq: in.MediaSeq,
		StartSeconds: st.GlobalSeconds, DriftPPM: p.DriftPPM,
		Damaged: ps.Damaged,
	}
	if ps.MP4 != nil {
		ns.Boxes = ps.MP4.Boxes
		ns.MediaSeqBox = ps.MP4.MediaSeq
		ns.BaseDTS = ps.MP4.BaseDTS
		ns.SafePrefix = ps.MP4.SafePrefix
	}
	timescale := p.DefaultTimes
	if ps.MP4 != nil && len(ps.MP4.Tracks) > 0 && ps.MP4.Tracks[0].Timescale > 0 {
		timescale = ps.MP4.Tracks[0].Timescale
	}
	ns.Timescale = timescale
	tl := clock.NewTimeline(timescale).SetDriftPPM(p.DriftPPM)

	var views []index.SampleView
	if ps.MP4 != nil {
		for _, s := range ps.MP4.Samples {
			corrected := tl.Correct(int64(s.DTS))
			absSeconds := st.GlobalSeconds + corrected.Seconds()
			ns.Samples = append(ns.Samples, NormalizedSample{
				Index: s.Index, TrackID: s.TrackID, DTS: s.DTS,
				Duration: s.Duration, Keyframe: s.Keyframe,
				ByteOffset: s.ByteOffset, Size: s.Size,
				TimeSeconds: round6(absSeconds),
			})
			views = append(views, index.SampleView{
				SampleIndex: s.Index, Keyframe: s.Keyframe,
				ByteOffset: s.ByteOffset, TimeSeconds: round6(absSeconds),
			})
		}
	}

	if ps.Damaged {
		// The damaged segment's samples are untrustworthy: keep them out
		// of the index rather than indexing partial, unverified data.
		ns.Quarantined = p.Index.Quarantine(in.Name)
	} else {
		st.PrevKeyTime = index.Build(p.Index, in.Name, in.MediaSeq,
			views, st.PrevKeyTime, st.HavePrevKey)
		if len(views) > 0 {
			st.HavePrevKey = true
		}
	}

	// Advance the running time line using this segment's measured span.
	span := segmentSpan(ps.MP4, timescale, tl)
	ns.StartSeconds = round6(st.GlobalSeconds)
	st.GlobalTicks += int64(math.Round(span.Seconds() * float64(timescale)))
	st.GlobalSeconds = round6(st.GlobalSeconds + span.Seconds())
	return ns, st
}

// segmentSpan computes corrected duration from actual sample durations
// (VFR-safe): the final sample DTS plus its duration minus the base.
func segmentSpan(mp *stream.MP4Result, timescale uint32, tl *clock.Timeline) clock.Rat {
	if mp == nil || len(mp.Samples) == 0 {
		return clock.Rat{}
	}
	last := mp.Samples[len(mp.Samples)-1]
	endUnits := int64(last.DTS) + int64(last.Duration) - int64(mp.BaseDTS)
	if endUnits < 0 {
		endUnits = 0
	}
	return tl.Correct(endUnits)
}

// StoreHash canonicalizes the normalized segment and records both hash
// classes. Damaged segments keep their input hash but no output hash.
func (p *Pipeline) StoreHash(ps *ParsedSegment, ns *NormalizedSegment) (string, error) {
	var outputHash string
	if !ps.Damaged {
		raw, err := Canonicalize(ns)
		if err != nil {
			return "", err
		}
		outputHash = report.Sum256(raw)
	}
	p.Hasher.Add(report.SegmentHash{
		Segment: ps.Input.Name, MediaSeq: ps.Input.MediaSeq,
		InputHash: ps.InputHash, OutputHash: outputHash,
		InputBytes: int64(len(ps.Input.Data)), Damaged: ps.Damaged,
	})
	return outputHash, nil
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
