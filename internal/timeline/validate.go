package timeline

import (
	"fmt"

	"github.com/zld/media-check/internal/clock"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
)

func validateSegmentSamples(seg mp4.Segment, track mp4.Track, samples map[int]mp4.Sample, r *Report) clock.Range {
	if seg.FirstSampleIndex < 0 || seg.LastSampleIndex < seg.FirstSampleIndex {
		r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeSampleOutOfRange,
			"segment sample index range invalid", fault.WithTrack(seg.TrackID)))
		return clock.Range{}
	}
	first, ok1 := samples[seg.FirstSampleIndex]
	last, ok2 := samples[seg.LastSampleIndex]
	if !ok1 || !ok2 {
		r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeSampleOutOfRange,
			fmt.Sprintf("segment references missing samples %d..%d", seg.FirstSampleIndex, seg.LastSampleIndex),
			fault.WithTrack(seg.TrackID)))
		return clock.Range{}
	}
	end, overflow := addTicks(last.PTS, last.Duration)
	if overflow {
		r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeSampleOutOfRange,
			"segment source end overflow", fault.WithTrack(seg.TrackID)))
		return clock.Range{}
	}
	sr, err := clock.NewRange(first.PTS, end, track.Timescale)
	if err != nil {
		r.Issues = append(r.Issues, fault.Wrap(fault.StageTimeline, fault.CodeSampleOutOfRange, err,
			"invalid segment source range", fault.WithTrack(seg.TrackID)))
		return clock.Range{}
	}
	if sr.End > track.Duration {
		r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeSampleOutOfRange,
			"segment source range exceeds track duration", fault.WithTrack(seg.TrackID), fault.WithTimeRange(sr)))
	}
	return sr
}

func validateTransitions(man *mp4.Manifest, segByID map[string]mp4.Segment, r *Report) {
	type pair struct{ a, b string }
	covered := map[string]mp4.Transition{}
	for _, tr := range man.Transitions {
		a, oka := segByID[tr.FromSegmentID]
		b, okb := segByID[tr.ToSegmentID]
		if !oka || !okb {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
				"transition references unknown segment"))
			continue
		}
		if a.TrackID != b.TrackID {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
				"transition segments must share a track", fault.WithTrack(a.TrackID)))
		}
		if tr.StartMS >= tr.EndMS {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
				"transition range is empty or reversed", fault.WithTrack(a.TrackID)))
		}
		lo := max64(a.TimelineStart, b.TimelineStart, tr.StartMS)
		hi := min64(a.TimelineEnd, b.TimelineEnd, tr.EndMS)
		if lo != tr.StartMS || hi != tr.EndMS || hi <= lo {
			trRange := clock.Range{Start: tr.StartMS, End: tr.EndMS, Timescale: r.TimelineScale}
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
				"transition must lie inside adjacent segment overlap",
				fault.WithTrack(a.TrackID), fault.WithTimeRange(trRange)))
		}
		key := pairKey(a.ID, b.ID)
		if _, exists := covered[key]; exists {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
				"duplicate transition for segment pair", fault.WithTrack(a.TrackID)))
		}
		covered[key] = tr
	}

	byTrack := map[uint32][]mp4.Segment{}
	for _, s := range man.Segments {
		byTrack[s.TrackID] = append(byTrack[s.TrackID], s)
	}
	for trackID, segs := range byTrack {
		for i := 0; i < len(segs); i++ {
			for j := i + 1; j < len(segs); j++ {
				a, b := segs[i], segs[j]
				lo := max64(a.TimelineStart, b.TimelineStart)
				hi := min64(a.TimelineEnd, b.TimelineEnd)
				if hi <= lo {
					continue
				}
				tr, ok := covered[pairKey(a.ID, b.ID)]
				if !ok || tr.StartMS > lo || tr.EndMS < hi {
					ov := clock.Range{Start: lo, End: hi, Timescale: r.TimelineScale}
					r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
						"overlapping segments require a covering transition",
						fault.WithTrack(trackID), fault.WithTimeRange(ov)))
				}
			}
		}
	}
}

func validateEdits(man *mp4.Manifest, r *Report) {
	for _, e := range man.Edits {
		if e.AtMS < 0 {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeEditOutOfRange,
				"edit point is before timeline start", fault.WithTrack(e.TrackID)))
			continue
		}
		insideTransition := e.AtMS == 0
		for _, tr := range man.Transitions {
			if e.AtMS >= tr.StartMS && e.AtMS <= tr.EndMS {
				insideTransition = true
			}
		}
		if !insideTransition {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidOverlap,
				"edit point must be at zero or inside a transition window",
				fault.WithTrack(e.TrackID),
				fault.WithTimeRange(clock.Range{Start: e.AtMS, End: e.AtMS, Timescale: r.TimelineScale})))
		}
	}
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

func addTicks(a, b int64) (int64, bool) {
	if b < 0 || a > (1<<63-1)-b {
		return 0, true
	}
	return a + b, false
}

func max64(values ...int64) int64 {
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func min64(values ...int64) int64 {
	m := values[0]
	for _, v := range values[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
