package demo

import (
	"mediapack/timeline"
)

// TimelineDef bundles the graph, clips and transitions of the demo session.
type TimelineDef struct {
	Graph       *timeline.Graph
	Clips       []timeline.Clip
	Transitions []timeline.Transition
}

// BuildTimeline constructs the demo track graph: two video sources and
// one audio track feeding the program output, with two overlapping
// crossfades between the video tracks. The audio track starts 0.5ms off
// the video grid to exercise non-zero track starts and skew reporting.
func BuildTimeline(in InputSet, fps int64, durationNanos int64) (*TimelineDef, error) {
	g := timeline.NewGraph()
	tracks := []*timeline.Track{
		{ID: "video-main", Kind: timeline.KindVideo, Source: in.VideoMP4, IntervalNanos: timeline.FrameInterval(fps)},
		{ID: "audio-main", Kind: timeline.KindAudio, Source: in.AudioMP4, StartNanos: 500000, IntervalNanos: timeline.TicksToNanos(1024, 48000)},
		{ID: "remote-rtp", Kind: timeline.KindRTPVideo, Source: in.RTPLog, IntervalNanos: timeline.FrameInterval(fps)},
		{ID: "program-out", Kind: timeline.KindOutput, DependsOn: []string{"video-main", "audio-main", "remote-rtp"}},
	}
	for _, t := range tracks {
		if err := g.Add(t); err != nil {
			return nil, err
		}
	}
	clips := []timeline.Clip{
		{TrackID: "video-main", SourceIn: 0, SourceOut: durationNanos, TimelineStart: 0},
		{TrackID: "audio-main", SourceIn: 0, SourceOut: durationNanos, TimelineStart: 500000},
		{TrackID: "remote-rtp", SourceIn: 0, SourceOut: durationNanos, TimelineStart: 0},
	}
	dur := timeline.FrameInterval(fps) * 10
	transitions := []timeline.Transition{
		{ID: "xf-1", FromTrack: "video-main", ToTrack: "remote-rtp", Start: timeline.FrameTime(0, fps, 20), Duration: dur, Kind: "crossfade"},
		{ID: "xf-2", FromTrack: "remote-rtp", ToTrack: "video-main", Start: timeline.FrameTime(0, fps, 25), Duration: dur, Kind: "crossfade"},
	}
	return &TimelineDef{Graph: g, Clips: clips, Transitions: transitions}, nil
}
