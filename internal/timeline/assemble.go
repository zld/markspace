// Package timeline assembles tracks, clip segments, edit points, and
// transitions on one integer millisecond timeline.
package timeline

import (
	"fmt"
	"sort"

	"github.com/zld/media-check/internal/clock"
	"github.com/zld/media-check/internal/fault"
	"github.com/zld/media-check/internal/mp4"
	"github.com/zld/media-check/internal/stream"
)

func Assemble(man *mp4.Manifest, scanRes *stream.Result) *Report {
	r := &Report{TimelineScale: mp4.TimelineScale}
	if man == nil {
		r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidArgument, "nil manifest"))
		return r
	}
	if man.TimelineScale != 0 {
		r.TimelineScale = man.TimelineScale
	}

	trackByID := map[uint32]mp4.Track{}
	samplesByTrackIndex := map[uint32]map[int]mp4.Sample{}
	for _, t := range man.Tracks {
		trackByID[t.TrackID] = t
	}
	for _, s := range man.Samples {
		if samplesByTrackIndex[s.TrackID] == nil {
			samplesByTrackIndex[s.TrackID] = map[int]mp4.Sample{}
		}
		samplesByTrackIndex[s.TrackID][s.Index] = s
	}

	maxDurationMS := int64(0)
	for _, t := range man.Tracks {
		if t.Timescale == 0 {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidTimescale,
				"track timescale must be positive", fault.WithTrack(t.TrackID)))
			continue
		}
		durMS, err := clock.Convert(t.Duration, t.Timescale, r.TimelineScale)
		if err != nil {
			r.Issues = append(r.Issues, fault.Wrap(fault.StageTimeline, fault.CodeInvalidTimescale, err,
				"cannot convert track duration", fault.WithTrack(t.TrackID)))
			continue
		}
		if durMS < 0 {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidTimescale,
				"negative track duration", fault.WithTrack(t.TrackID)))
		}
		if durMS > maxDurationMS {
			maxDurationMS = durMS
		}
		count := len(samplesByTrackIndex[t.TrackID])
		if scanRes != nil {
			for _, tr := range scanRes.Tracks {
				if tr.TrackID == t.TrackID {
					count = tr.TotalSamples
				}
			}
		}
		r.Tracks = append(r.Tracks, TrackView{
			TrackID: t.TrackID, Kind: t.Kind, Timescale: t.Timescale, DurationMS: durMS,
			SampleCount:   count,
			TimelineRange: clock.Range{Start: 0, End: durMS, Timescale: r.TimelineScale},
		})
	}

	segByID := map[string]mp4.Segment{}
	materialRefs := map[string][]string{}
	for _, seg := range man.Segments {
		segByID[seg.ID] = seg
		materialRefs[seg.MaterialID] = append(materialRefs[seg.MaterialID], seg.ID)
		track, ok := trackByID[seg.TrackID]
		if !ok {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeInvalidArgument,
				"segment references unknown track", fault.WithTrack(seg.TrackID)))
			continue
		}
		tr, err := clock.NewRange(seg.TimelineStart, seg.TimelineEnd, r.TimelineScale)
		if err != nil {
			r.Issues = append(r.Issues, fault.Wrap(fault.StageTimeline, fault.CodeEditOutOfRange, err,
				"invalid segment timeline range", fault.WithTrack(seg.TrackID)))
			continue
		}
		durMS, _ := clock.Convert(track.Duration, track.Timescale, r.TimelineScale)
		if tr.End > durMS {
			r.Issues = append(r.Issues, fault.E(fault.StageTimeline, fault.CodeEditOutOfRange,
				fmt.Sprintf("segment ends at %dms beyond track duration %dms", tr.End, durMS),
				fault.WithTrack(seg.TrackID), fault.WithTimeRange(tr)))
		}
		sourceRange := validateSegmentSamples(seg, track, samplesByTrackIndex[seg.TrackID], r)
		r.Segments = append(r.Segments, SegmentView{Segment: seg, TimeRange: tr, SourceTimeRange: sourceRange})
	}

	for material, ids := range materialRefs {
		if material == "" || len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		r.DuplicateGroups = append(r.DuplicateGroups, DuplicateGroup{MaterialID: material, Count: len(ids), SegmentIDs: ids})
	}
	sort.Slice(r.DuplicateGroups, func(i, j int) bool { return r.DuplicateGroups[i].MaterialID < r.DuplicateGroups[j].MaterialID })

	validateTransitions(man, segByID, r)
	validateEdits(man, r)
	r.Alignment = alignTracks(man)
	return r
}

func alignTracks(man *mp4.Manifest) AlignmentReport {
	var video, audio []mp4.Sample
	for _, s := range man.Samples {
		if s.TrackID == 1 {
			video = append(video, s)
		}
		if s.TrackID == 2 {
			audio = append(audio, s)
		}
	}
	n := len(video)
	if len(audio) < n {
		n = len(audio)
	}
	rep := AlignmentReport{Pairs: n}
	for i := 0; i < n; i++ {
		vMS, err1 := clock.Convert(video[i].PTS, 90000, 1000)
		aMS, err2 := clock.Convert(audio[i].PTS, 48000, 1000)
		if err1 != nil || err2 != nil {
			continue
		}
		d := vMS - aMS
		if d < 0 {
			d = -d
		}
		if d > rep.MaxErrorMS {
			rep.MaxErrorMS = d
		}
	}
	return rep
}
