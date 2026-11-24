package pipeline

import (
	"fmt"

	"mediarc/internal/ingest"
	"mediarc/internal/media"
	"mediarc/internal/model"
	"mediarc/internal/normalize"
	"mediarc/internal/recovery"
	"mediarc/internal/syncer"
)

type chunkBuildConfig struct {
	FramePath    string
	TargetRate   uint32
	SourceLayout normalize.Layout
	TargetLayout normalize.Layout
	Routes       []normalize.Route
	Clock        model.ClockMap
	FrameDelta   uint32
	Width        uint16
	Height       uint16
	Sync         bool
	Faults       *model.Collector
}

func reconcileColor(o Options, metas []ingest.FrameMeta, faults *model.Collector) normalize.ColorReport {
	var sources []model.SourcedColor
	for _, m := range metas {
		if m.Color.Primaries != "" {
			sources = append(sources, model.SourcedColor{
				Source: "mrc1-frame-header", Color: m.Color, Offset: m.Offset,
			})
			break
		}
	}
	if o.HasInject("color-conflict") || o.HasInject("color_conflict") {
		sources = append(sources, model.SourcedColor{
			Source: "conflicting-sidecar",
			Color:  model.ColorMetadata{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "full"},
		})
	}
	return normalize.ReconcileColor(sources, faults)
}

// buildRangeMP4 creates one deterministic MP4 for frames [start,end) and audio
// blocks whose mapped PTS falls in the same interval.
func buildRangeMP4(frames []recovery.RestoredFrame, start, end int, audio []recovery.AudioBlock, cfg chunkBuildConfig) ([]byte, int, error) {
	if cfg.FrameDelta == 0 {
		cfg.FrameDelta = model.VideoFrameDelta
	}
	video := media.Track{ID: 1, Kind: "video", Timescale: model.VideoTimescale,
		Width: cfg.Width, Height: cfg.Height}
	if video.Width == 0 {
		video.Width = 320
	}
	if video.Height == 0 {
		video.Height = 180
	}
	for i := start; i < end && i < len(frames); i++ {
		rf := frames[i]
		data := rf.Marker
		sync := i%8 == 0
		if rf.State == recovery.FramePresent && rf.Winner != nil {
			payload, err := ingest.ReadFramePayload(cfg.FramePath, *rf.Winner)
			if err != nil {
				data = recovery.MissingFrameMarker(i)
				cfg.Faults.Add(model.Fault{Kind: model.FaultBadSHA, RecordIndex: int64(i),
					Offset: rf.Winner.PayloadOffset, Message: err.Error(), Recoverable: true})
			} else {
				data = payload
				sync = rf.Winner.KeyFrame
				if rf.Winner.Width != 0 {
					video.Width = rf.Winner.Width
					video.Height = rf.Winner.Height
				}
			}
		}
		video.Samples = append(video.Samples, media.Sample{Data: data, Delta: rf.Duration, Sync: sync})
	}

	audioTrack := media.Track{ID: 2, Kind: "audio", Timescale: cfg.TargetRate,
		Channels: uint16(len(cfg.TargetLayout.Channels))}
	if audioTrack.Timescale == 0 {
		audioTrack.Timescale = model.AudioClockRate
	}
	startPTS := uint64(start) * uint64(cfg.FrameDelta)
	endPTS := uint64(end) * uint64(cfg.FrameDelta)
	alignedFirst := false
	for _, block := range audio {
		mapped := cfg.Clock.Map(uint64(block.Timestamp))
		if mapped < startPTS || mapped >= endPTS {
			continue
		}
		if cfg.Sync && !alignedFirst {
			if _, err := syncer.Align(block.Timestamp, startPTS, syncer.Config{
				Mapping: cfg.Clock, FrameDelta: cfg.FrameDelta,
			}, cfg.Faults); err != nil {
				return nil, 0, err
			}
			alignedFirst = true
		}
		result, err := normalize.NormalizeAudio(block.Payload, normalize.AudioSpec{
			InputRate: cfg.Clock.SourceRate, OutputRate: cfg.TargetRate,
			Input: cfg.SourceLayout, Output: cfg.TargetLayout, Routes: cfg.Routes,
		})
		if err != nil {
			return nil, 0, err
		}
		encoded := normalize.EncodeS16BE(result.Samples)
		frames := 0
		if len(cfg.TargetLayout.Channels) > 0 {
			frames = len(result.Samples) / len(cfg.TargetLayout.Channels)
		}
		audioTrack.Samples = append(audioTrack.Samples, media.Sample{Data: encoded, Delta: uint32(frames)})
	}
	b, err := media.BuildMP4([]media.Track{video, audioTrack})
	if err != nil {
		return nil, 0, fmt.Errorf("build mp4 range %d-%d: %w", start, end, err)
	}
	return b, len(audioTrack.Samples), nil
}
