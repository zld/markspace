// Package demo constructs the deterministic end-to-end media and fault set
// used by the acceptance command. All fixtures are generated in Go.
package demo

import (
	"math"
	"path/filepath"

	"mediapack/internal/batch"
	"mediapack/internal/clock"
	"mediapack/internal/demux"
	"mediapack/internal/fixture"
	"mediapack/internal/maptrack"
	"mediapack/internal/timeline"
)

const (
	RTPSRC = 0x52545031
	RTPPT  = 111
)

type Paths struct {
	Dir             string
	MP4             string
	MOV             string
	RTP             string
	RTPGap          string
	TruncatedBox    string
	CorruptSample   string
	TruncatedSample string
}

func Generate(out string, chunkSamples int) (batch.Config, Paths, error) {
	inDir := filepath.Join(out, "inputs")
	p := Paths{
		Dir:             inDir,
		MP4:             filepath.Join(inDir, "normal.mp4"),
		MOV:             filepath.Join(inDir, "normal.mov"),
		RTP:             filepath.Join(inDir, "rtp_wrap.rtp"),
		RTPGap:          filepath.Join(inDir, "rtp_gap.rtp"),
		TruncatedBox:    filepath.Join(inDir, "truncated_box.mp4"),
		CorruptSample:   filepath.Join(inDir, "corrupt_sample.mp4"),
		TruncatedSample: filepath.Join(inDir, "truncated_sample.mp4"),
	}

	_, err := fixture.WriteMP4(p.MP4, fixture.MP4Spec{Tracks: []fixture.TrackSpec{
		{ID: 1, Kind: "video", Timescale: 24000, SampleDuration: 1000, SampleCount: 24, PayloadSize: 512, Width: 320, Height: 180},
		{ID: 2, Kind: "audio", Timescale: 48000, SampleDuration: 1024, SampleCount: 48, PayloadSize: 256, Channels: 2, SampleRate: 48000},
	}})
	if err != nil {
		return batch.Config{}, p, err
	}
	_, err = fixture.WriteMP4(p.MOV, fixture.MP4Spec{Brand: "qt  ", Tracks: []fixture.TrackSpec{
		{ID: 1, Kind: "video", Timescale: 24000, SampleDuration: 1000, SampleCount: 12, PayloadSize: 256, Width: 160, Height: 90},
	}})
	if err != nil {
		return batch.Config{}, p, err
	}

	rtpStart := uint32(math.MaxUint32 - 1000)
	packets := fixture.WrappedTimestampPackets(RTPSRC, RTPPT, 24, rtpStart, 960)
	if err := fixture.WriteRTPFrames(p.RTP, packets); err != nil {
		return batch.Config{}, p, err
	}
	if err := fixture.WriteRTPFrames(p.RTPGap, fixture.GapPackets(42, 96)); err != nil {
		return batch.Config{}, p, err
	}
	if err := fixture.WriteTruncatedBox(p.TruncatedBox); err != nil {
		return batch.Config{}, p, err
	}
	if _, err := fixture.WriteMP4(p.CorruptSample, fixture.MP4Spec{
		Tracks: []fixture.TrackSpec{
			{ID: 1, Kind: "video", Timescale: 24000, SampleDuration: 1000, SampleCount: 8, PayloadSize: 64},
		},
		Corrupt: []fixture.SampleRef{{TrackID: 1, Index: 3}},
	}); err != nil {
		return batch.Config{}, p, err
	}
	if _, err := fixture.WriteMP4(p.TruncatedSample, fixture.MP4Spec{
		Tracks: []fixture.TrackSpec{
			{ID: 2, Kind: "audio", Timescale: 48000, SampleDuration: 1024, SampleCount: 8, PayloadSize: 48, Channels: 1, SampleRate: 48000},
		},
		Oversize:      []fixture.SampleRef{{TrackID: 2, Index: 7}},
		OversizeBytes: 1 << 20,
	}); err != nil {
		return batch.Config{}, p, err
	}

	rtpIn := clock.Ticks(int64(rtpStart), clock.TickRate(48000))
	rtpOut := clock.Ticks(int64(rtpStart)+24*960, clock.TickRate(48000))
	cfg := batch.Config{
		OutDir:           out,
		ChunkSamples:     chunkSamples,
		TargetBitrateBps: 208000,
		MP4Inputs:        []string{p.MP4, p.MOV, p.CorruptSample, p.TruncatedSample, p.TruncatedBox},
		RTPInputs:        []string{p.RTP, p.RTPGap},
		RTPClockRate:     48000,
		Rules: []maptrack.Rule{
			{Input: p.MP4, TrackID: 1, TargetID: 1, TargetLabel: "main-video"},
			{Input: p.MP4, TrackID: 2, TargetID: 2, TargetLabel: "main-audio"},
			{Input: p.MOV, TrackID: 1, TargetID: 3, TargetLabel: "mov-transition"},
			{Input: p.RTP, TargetID: 4, TargetLabel: "rtp-audio",
				RTP: &demux.RTPSourceKey{SSRC: RTPSRC, PayloadType: RTPPT}},
		},
		Timeline: timeline.Spec{
			Clips: []timeline.ClipSpec{
				{ID: "main-video", TargetID: 1, Group: "vision", Start: clock.New(0, 1),
					In: clock.New(2000, 24000), Out: clock.New(22000, 24000)},
				{ID: "main-audio", TargetID: 2, Group: "audio", Start: clock.New(0, 1),
					In: clock.New(0, 48000), Out: clock.New(48*1024, 48000)},
				{ID: "mov-overlap", TargetID: 3, Group: "vision", Start: clock.New(0, 1),
					In: clock.New(0, 24000), Out: clock.New(12000, 24000)},
				{ID: "rtp-wrap", TargetID: 4, Group: "rtp", Start: clock.New(0, 1),
					In: rtpIn, Out: rtpOut},
			},
			Transitions: []timeline.TransitionSpec{{
				ID: "cross-dissolve", Kind: "dissolve",
				FromClipID: "main-video", ToClipID: "mov-overlap",
				Overlap: clock.New(1, 2),
			}},
		},
	}
	return cfg, p, nil
}
