// Package samplegen creates all local code-generated acceptance fixtures.
package samplegen

import (
	"fmt"

	"github.com/pion/rtp"

	"archivecheck/internal/clock"
	"archivecheck/internal/media"
	"archivecheck/internal/policy"
	"archivecheck/internal/subtitle"
)

// Sample is one generated archive asset plus its expected alignment signal.
type Sample struct {
	Index          int
	ID             string
	Kind           string
	InputName      string
	OutputName     string
	ExpectedTimeNS clock.Nanoseconds
	Data           []byte
	Timecode       string
	Rate           clock.Rate
	RTPBytes       []byte
	RTPClockRate   uint32
	RTPExpectedTS  uint32
	Description    media.TrackDescription
	Quality        policy.Quality
	Constraints    policy.Constraints
}

// All returns the deterministic demo batch. Valid items precede injected
// failures so cancellation demos have stable first completed checkpoints.
func All(seed int64, budgetBytes int64) []Sample {
	_ = seed
	samples := []Sample{
		srtValid(),
		vttValid(),
		timecode24(),
		timecodeDF(),
		mp4Audio(budgetBytes),
		layoutRemap(budgetBytes),
		rtpValid(budgetBytes),
		truncatedSubtitle(),
		illegalDF(),
		metadataConflict(budgetBytes),
		rtpBadTimestamp(),
		oversizedLine(),
		budgetOverrun(budgetBytes),
	}
	for i := range samples {
		samples[i].Index = i
		if samples[i].Description.Timescale != 0 {
			samples[i].Description.Index = i
		}
		if samples[i].Constraints.SourceOffset == 0 {
			samples[i].Constraints.SourceOffset = samples[i].Description.MetadataOffset
		}
		if samples[i].InputName == "" {
			samples[i].InputName = fmt.Sprintf("sample-%02d-%s.in", i, samples[i].ID)
		}
		if samples[i].OutputName == "" {
			samples[i].OutputName = fmt.Sprintf("out/sample-%02d-%s.out", i, samples[i].ID)
		}
		if samples[i].Constraints.BudgetBytes == 0 && budgetBytes > 0 {
			samples[i].Constraints.BudgetBytes = budgetBytes
		}
	}
	return samples
}

func srtValid() Sample {
	data := []byte("1\n00:00:00,500 --> 00:00:02,000\nArchive caption one\n\n" +
		"2\n00:00:01,000 --> 00:00:03,500\nArchive caption two\n")
	return Sample{
		ID: "srt-valid", Kind: "subtitle", Data: data,
		ExpectedTimeNS: 500 * clock.Millisecond,
		Description: media.TrackDescription{
			Kind: "subtitle", Codec: "srt", Timescale: uint32(clock.Second),
			DurationNS: int64(4 * clock.Second),
		},
		Quality:     policy.Quality{Score: 91},
		Constraints: policy.Constraints{QualityFloor: 80},
	}
}

func vttValid() Sample {
	data := []byte("WEBVTT\n\n00:01.500 --> 00:03.000\nWebVTT restored asset\n")
	return Sample{
		ID: "vtt-valid", Kind: "subtitle", Data: data,
		ExpectedTimeNS: clock.Second + 500*clock.Millisecond,
		Description: media.TrackDescription{
			Kind: "subtitle", Codec: "vtt", Timescale: uint32(clock.Second),
			DurationNS: int64(4 * clock.Second),
		},
		Quality:     policy.Quality{Score: 88},
		Constraints: policy.Constraints{QualityFloor: 80},
	}
}

func timecode24() Sample {
	return Sample{
		ID: "timecode-24", Kind: "timecode", Timecode: "00:00:01:00", Rate: clock.Rate24,
		ExpectedTimeNS: clock.Second,
		Description: media.TrackDescription{
			Kind: "timecode", Codec: "smpte", Timescale: uint32(clock.Second),
			DurationNS: int64(2 * clock.Second),
		},
		Quality:     policy.Quality{Score: 95},
		Constraints: policy.Constraints{},
	}
}

func timecodeDF() Sample {
	return Sample{
		ID: "timecode-df", Kind: "timecode", Timecode: "00:01:00;02", Rate: clock.Rate2997D,
		ExpectedTimeNS: 60_060_000_000,
		Description: media.TrackDescription{
			Kind: "timecode", Codec: "smpte-df", Timescale: uint32(clock.Second),
			DurationNS: int64(61 * clock.Second),
		},
		Quality:     policy.Quality{Score: 90},
		Constraints: policy.Constraints{},
	}
}

func mp4Audio(budget int64) Sample {
	spec := media.MP4Spec{TrackID: 7, Timescale: 48000, DurationTicks: 96000, Channels: 2, SampleRate: 48000}
	return Sample{
		ID: "mp4-audio", Kind: "mp4", Data: media.GenerateMinimalMP4(spec), ExpectedTimeNS: 2 * clock.Second,
		Quality:     policy.Quality{Score: 84, SourceBitrateBps: 192_000},
		Constraints: policy.Constraints{QualityFloor: 80, BudgetBytes: budget},
	}
}

func layoutRemap(budget int64) Sample {
	color := media.ColorMetadata{ColourType: "nclx", Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false}
	target := media.ColorMetadata{ColourType: "nclx", Primaries: 9, Transfer: 16, Matrix: 9}
	desc := media.TrackDescription{
		Kind: "video", Codec: "avc1", Timescale: 30000, DurationNS: int64(2 * clock.Second),
		Width: 1280, Height: 720, Channels: 6, ChannelLayout: media.Layout51, SampleRate: 48000,
		Color: &color, MetadataOffset: 192,
	}
	return Sample{
		ID: "layout-remap", Kind: "description", Description: desc, ExpectedTimeNS: 2 * clock.Second,
		Quality: policy.Quality{Score: 92, SourceBitrateBps: 2_000_000},
		Constraints: policy.Constraints{
			QualityFloor: 80, BudgetBytes: budget,
			Layout: media.RequestedLayout{
				Channels: 2, ChannelLayout: media.LayoutStereo, ChannelMappingID: "archive-5.1-to-stereo-v1",
				SampleRate: 44100, ResampleAlgorithm: "rational-ns-v1",
				Color: &target, ColorConversionID: "bt709-to-bt2020-v1",
			},
		},
	}
}

func rtpValid(budget int64) Sample {
	data, _ := (&rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: 100, Timestamp: 90000, SSRC: 0xabcdef,
	}, Payload: []byte("code-generated access unit")}).Marshal()
	return Sample{
		ID: "rtp-valid", Kind: "rtp", RTPBytes: data, RTPClockRate: 90000, RTPExpectedTS: 90000,
		ExpectedTimeNS: clock.Second,
		Description: media.TrackDescription{
			Kind: "audio", Codec: "rtp-pcma", Timescale: 90000, DurationNS: int64(clock.Second),
			Channels: 2, ChannelLayout: media.LayoutStereo, SampleRate: 48000,
		},
		Quality:     policy.Quality{Score: 86},
		Constraints: policy.Constraints{BudgetBytes: budget},
	}
}

func truncatedSubtitle() Sample {
	return Sample{ID: "subtitle-truncated", Kind: "srt-truncated", Data: []byte("1\n00:00:01,000 --> 00:00:0")}
}

func illegalDF() Sample {
	return Sample{ID: "illegal-df-number", Kind: "timecode-illegal-df", Timecode: "00:01:00;00", Rate: clock.Rate2997D}
}

func metadataConflict(budget int64) Sample {
	desc := media.TrackDescription{
		Kind: "audio", Codec: "mp4a", Timescale: 48000, DurationNS: int64(2 * clock.Second),
		Channels: 6, ChannelLayout: media.Layout51, SampleRate: 48000, MetadataOffset: 256,
	}
	return Sample{
		ID: "metadata-conflict", Kind: "description", Description: desc,
		Quality:     policy.Quality{Score: 90},
		Constraints: policy.Constraints{BudgetBytes: budget, Layout: media.RequestedLayout{Channels: 2, ChannelLayout: media.LayoutStereo}},
	}
}

func rtpBadTimestamp() Sample {
	data, _ := (&rtp.Packet{Header: rtp.Header{
		Version: 2, PayloadType: 96, SequenceNumber: 101, Timestamp: 80000, SSRC: 0x123456,
	}, Payload: []byte("bad timestamp sample")}).Marshal()
	return Sample{ID: "rtp-bad-timestamp", Kind: "rtp-bad", RTPBytes: data, RTPClockRate: 90000, RTPExpectedTS: 90000}
}

func oversizedLine() Sample {
	return Sample{
		ID: "subtitle-oversized-line", Kind: "srt-oversized",
		Data: []byte("1\n" + longX(subtitle.DefaultMaxLineBytes+64) + "\n"),
	}
}

func budgetOverrun(budget int64) Sample {
	desc := media.TrackDescription{
		Kind: "video", Codec: "avc1", Timescale: 30000, DurationNS: int64(30 * clock.Second),
		Width: 1920, Height: 1080, Channels: 2, ChannelLayout: media.LayoutStereo, SampleRate: 48000,
		MetadataOffset: 320,
	}
	return Sample{
		ID: "budget-overrun", Kind: "description", Description: desc,
		Quality:     policy.Quality{Score: 35, SourceBitrateBps: 4_000_000},
		Constraints: policy.Constraints{BudgetBytes: budget, QualityFloor: 80},
	}
}

func longX(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'X'
	}
	return string(b)
}
