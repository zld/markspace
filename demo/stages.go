package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"mediapack/manifest"
	"mediapack/media"
	"mediapack/merr"
	"mediapack/pipeline"
	"mediapack/render"
	"mediapack/timeline"
)

// Stage names in pipeline order.
const (
	StageIngest   = "ingest"
	StageTimeline = "timeline"
	StageRender   = "render"
	StageManifest = "manifest"
)

type ingestPayload struct {
	Video     media.Track      `json:"video"`
	Audio     media.Track      `json:"audio"`
	RTPFrames []media.RTPFrame `json:"rtp_frames"`
	RTPStats  media.RTPStats   `json:"rtp_stats"`
}

type timelinePayload struct {
	Order        []string              `json:"order"`
	Start        int64                 `json:"start"`
	End          int64                 `json:"end"`
	FPS          int64                 `json:"fps"`
	FrameCount   int                   `json:"frame_count"`
	MaxSkewNanos int64                 `json:"max_skew_nanos"`
	Tracks       []manifest.TrackEntry `json:"tracks"`
	Transitions  []timeline.Transition `json:"transitions"`
	Clips        []timeline.Clip       `json:"clips"`
}

type renderPayload struct {
	Frames []manifest.FrameEntry `json:"frames"`
	Stats  render.Stats          `json:"stats"`
}

type manifestPayload struct {
	Digest       string              `json:"digest"`
	MaxSkewNanos int64               `json:"max_skew_nanos"`
	Stats        manifest.StatsBlock `json:"stats"`
	Path         string              `json:"path"`
}

func trackEntry(id, kind, path string, start, interval int64, samples int, corrupt int64) manifest.TrackEntry {
	b, _ := os.ReadFile(path)
	sum := sha256.Sum256(b)
	return manifest.TrackEntry{
		ID: id, Kind: kind, SourceDigest: hex.EncodeToString(sum[:]),
		StartNanos: start, IntervalNanos: interval, Samples: samples, CorruptOffset: corrupt,
	}
}

// Stages assembles the four pipeline stages of the demo run.
func Stages(in InputSet, def *TimelineDef, params Params, outDir string) []pipeline.Stage {
	start := int64(0)
	end := params.DurationSecs * timeline.NanosPerSecond

	ingest := pipeline.Stage{Name: StageIngest, Run: func(deps map[string]json.RawMessage) (any, error) {
		vf, err := media.ReadFile(in.VideoMP4)
		if err != nil {
			return nil, err
		}
		af, err := media.ReadFile(in.AudioMP4)
		if err != nil {
			return nil, err
		}
		if len(vf.Tracks) == 0 || len(af.Tracks) == 0 {
			return nil, merr.Corruptf("demux", "", "no tracks in generated inputs")
		}
		pkts, err := media.ReadRTPLog(in.RTPLog)
		if err != nil {
			return nil, err
		}
		frames, stats, err := media.ProcessRTP(pkts, media.RTPConfig{
			ClockRate: rtpClockRate, ReorderWindow: rtpReorderWin, GapThreshold: rtpGapThreshold,
		})
		if err != nil {
			return nil, err
		}
		return &ingestPayload{Video: *vf.Tracks[0], Audio: *af.Tracks[0], RTPFrames: frames, RTPStats: stats}, nil
	}}

	tl := pipeline.Stage{Name: StageTimeline, Run: func(deps map[string]json.RawMessage) (any, error) {
		var ing ingestPayload
		if err := pipeline.Decode(deps, StageIngest, &ing); err != nil {
			return nil, err
		}
		order, err := def.Graph.Order()
		if err != nil {
			return nil, err
		}
		if err := timeline.ValidateTransitions(def.Transitions, def.Graph); err != nil {
			return nil, err
		}
		for _, c := range def.Clips {
			if err := c.Validate(); err != nil {
				return nil, err
			}
		}
		count, err := timeline.FrameCount(start, end, params.FPS)
		if err != nil {
			return nil, err
		}
		tracks := []manifest.TrackEntry{
			trackEntry("video-main", "video", in.VideoMP4, 0, timeline.FrameInterval(params.FPS), len(ing.Video.Samples), ing.Video.CorruptOffset),
			trackEntry("audio-main", "audio", in.AudioMP4, 500000, timeline.TicksToNanos(1024, 48000), len(ing.Audio.Samples), ing.Audio.CorruptOffset),
			trackEntry("remote-rtp", "rtp-video", in.RTPLog, 0, timeline.FrameInterval(params.FPS), len(ing.RTPFrames), -1),
		}
		return &timelinePayload{
			Order: order, Start: start, End: end, FPS: params.FPS, FrameCount: count,
			MaxSkewNanos: manifest.MaxSkew(tracks, start),
			Tracks:       tracks, Transitions: def.Transitions, Clips: def.Clips,
		}, nil
	}}

	rnd := pipeline.Stage{Name: StageRender, Run: func(deps map[string]json.RawMessage) (any, error) {
		var ing ingestPayload
		if err := pipeline.Decode(deps, StageIngest, &ing); err != nil {
			return nil, err
		}
		var tp timelinePayload
		if err := pipeline.Decode(deps, StageTimeline, &tp); err != nil {
			return nil, err
		}
		sources := buildSources(&ing, &tp)
		comp := &render.Compositor{Width: params.Width, Height: params.Height, FPS: params.FPS, Seed: params.Seed}
		res, err := comp.Render(sources, tp.Transitions, tp.Start, tp.End)
		if err != nil {
			return nil, err
		}
		frames := make([]manifest.FrameEntry, len(res.Frames))
		for i, f := range res.Frames {
			frames[i] = manifest.FrameEntry{Index: f.Index, TimeNanos: f.Time, Valid: f.Valid, Hash: render.HashHex(f.Hash), Size: f.Size}
		}
		return &renderPayload{Frames: frames, Stats: res.Stats}, nil
	}}

	man := pipeline.Stage{Name: StageManifest, Run: func(deps map[string]json.RawMessage) (any, error) {
		var tp timelinePayload
		if err := pipeline.Decode(deps, StageTimeline, &tp); err != nil {
			return nil, err
		}
		var rp renderPayload
		if err := pipeline.Decode(deps, StageRender, &rp); err != nil {
			return nil, err
		}
		actualBps := rp.Stats.SampleBytes * 8 * timeline.NanosPerSecond / (tp.End - tp.Start)
		m := &manifest.Manifest{
			Version: 1, Timebase: "ns",
			StartNanos: tp.Start, EndNanos: tp.End,
			FrameIntervalNanos: timeline.FrameInterval(tp.FPS), FPS: tp.FPS,
			MaxSkewNanos: tp.MaxSkewNanos,
			Stats: manifest.StatsBlock{
				Frames: rp.Stats.Frames, ValidFrames: rp.Stats.ValidFrames, GapFrames: rp.Stats.GapFrames,
				QualityMean: rp.Stats.QualityMean, TargetBps: params.TargetBps, ActualBps: actualBps,
			},
			Tracks: tp.Tracks, Frames: rp.Frames,
		}
		if err := manifest.Verify(m); err != nil {
			return nil, err
		}
		path, err := manifest.Write(m, outDir, "artifacts/manifest.json")
		if err != nil {
			return nil, err
		}
		return &manifestPayload{Digest: m.Digest(), MaxSkewNanos: m.MaxSkewNanos, Stats: m.Stats, Path: path}, nil
	}}

	return []pipeline.Stage{ingest, tl, rnd, man}
}

// buildSources maps demuxed tracks onto the output grid through their clips.
func buildSources(ing *ingestPayload, tp *timelinePayload) []render.TrackSource {
	clipOf := func(id string) timeline.Clip {
		for _, c := range tp.Clips {
			if c.TrackID == id {
				return c
			}
		}
		return timeline.Clip{}
	}
	vc := clipOf("video-main")
	var vframes []render.SourceFrame
	for _, s := range ing.Video.Samples {
		srcT := timeline.TicksToNanos(s.DTS, int64(ing.Video.Timescale))
		if !vc.Contains(srcT) {
			continue
		}
		vframes = append(vframes, render.SourceFrame{Time: vc.TimelineStart + srcT - vc.SourceIn, Data: s.Data, Size: int(s.Size)})
	}
	rc := clipOf("remote-rtp")
	var rframes []render.SourceFrame
	for _, f := range ing.RTPFrames {
		t := rc.TimelineStart + f.TimeNanos
		if t < rc.TimelineStart || t >= rc.TimelineStart+rc.Duration() {
			continue
		}
		rframes = append(rframes, render.SourceFrame{Time: t, Data: f.Payload, Size: len(f.Payload)})
	}
	return []render.TrackSource{
		{TrackID: "video-main", Video: true, Required: true, Frames: vframes},
		{TrackID: "remote-rtp", Video: true, Required: true, Frames: rframes},
		{TrackID: "audio-main", Video: false},
	}
}
