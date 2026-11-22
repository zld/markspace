package fixture

import (
	"fmt"

	"github.com/pion/rtp"

	"example.com/mediatimeline/internal/ingest"
	"example.com/mediatimeline/internal/model"
	"example.com/mediatimeline/internal/pipeline"
)

// Fault identifies a synthetic failure mode. The demo CLI accepts these as a
// comma-separated list.
type Fault string

// Supported faults.
const (
	FaultWrap          Fault = "wrap"
	FaultLoss          Fault = "loss"
	FaultColorConflict Fault = "color-conflict"
	FaultBackward      Fault = "backward"
	FaultAudioGap      Fault = "audio-gap"
	FaultAudioOverlap  Fault = "audio-overlap"
	FaultAudioLoss     Fault = "audio-loss"
	FaultAudioShort    Fault = "audio-short"
	FaultCorruptLayer  Fault = "corrupt-layer"
)

// ScenarioOptions configures the whole synthetic program.
type ScenarioOptions struct {
	Seed      int64
	Faults    map[Fault]bool
	Ladder    int
	SegmentMS int64
	// Frames is the number of video frames (one access unit each).
	Frames int
	// FPS is the video frame rate numerator/denominator, e.g. 30/1.
	FPSNum, FPSDen int64
}

// DefaultOptions is the demo's --ladder 3 --segment-ms 2000 baseline.
func DefaultOptions(seed int64) ScenarioOptions {
	return ScenarioOptions{
		Seed: seed, Ladder: 3, SegmentMS: 2000, Frames: 90, FPSNum: 30, FPSDen: 1,
		Faults: map[Fault]bool{},
	}
}

// Scenario is all input material for one run plus the reference expectations
// computed from clean, fault-free values.
type Scenario struct {
	Options ScenarioOptions
	// MP4 is a video track (and an audio track when audio is enabled).
	MP4 []byte
	// VideoRTP/AudioRTP are wire packets, in observation order.
	VideoRTP [][]byte
	AudioRTP [][]byte
	// Metadata carriers.
	WebVTT   []byte
	Chapters []byte
	ID3      [][]byte
	ID3Ticks []int64
	Emsg     [][]byte
	SEI      []byte
	// Reference values for invariant checks.
	Reference Reference
	// LayerOverrides describes per-layer damage applied by the fan-out.
	LayerOverrides []LayerOverride
}

// LayerOverride is per-ladder-layer damage. Corrupt makes the layer's sample
// data unusable so the fan-out must exclude it without disturbing the others.
type LayerOverride struct {
	Index   int
	Corrupt bool
	// DropEvery removes one sample payload every N samples (0 disables).
	DropEvery int
}

// Reference holds the ground truth used to compute the demo metrics.
type Reference struct {
	VideoSamples  int
	AudioSamples  int
	Events        int
	VideoTimes    []int64 // repaired video ticks at 90 kHz, in order
	KeyframeTicks []int64
	Description   string
}

// BuildScenario synthesizes the program described by opts.
// BuildCorruptedLayerScenario is BuildScenario with the corrupt-layer fault
// forced on. Tests use it to prove per-layer isolation without going through
// the CLI flag parsing.
func BuildCorruptedLayerScenario(opts ScenarioOptions) (Scenario, error) {
	if opts.Faults == nil {
		opts.Faults = map[Fault]bool{}
	}
	opts.Faults[FaultCorruptLayer] = true
	return BuildScenario(opts)
}

func BuildScenario(opts ScenarioOptions) (Scenario, error) {
	if opts.Frames <= 0 {
		return Scenario{}, fmt.Errorf("frames must be positive")
	}
	if opts.Ladder < 1 || opts.Ladder > 8 {
		return Scenario{}, fmt.Errorf("ladder must be in [1,8]")
	}
	if opts.SegmentMS <= 0 {
		return Scenario{}, fmt.Errorf("segment-ms must be positive")
	}
	s := Scenario{Options: opts}
	rng := NewRand(opts.Seed)

	frameTicks := int64(90000) * opts.FPSDen / opts.FPSNum
	frames := opts.Frames
	// Video: one sync sample per second of program time.
	syncEvery := int(int64(90000) / frameTicks)
	if syncEvery < 1 {
		syncEvery = 1
	}

	videoColor := &model.ColorDeclaration{Source: model.ColorFromContainer,
		Primaries: 1, Transfer: 1, Matrix: 1}
	if opts.Faults[FaultColorConflict] {
		// Container says BT.709 limited; the bitstream will say BT.2020.
		videoColor = &model.ColorDeclaration{Source: model.ColorFromContainer,
			Primaries: 1, Transfer: 1, Matrix: 1}
	}

	video := TrackSpec{
		TrackID: model.TrackVideoMain, Timescale: 90000, Handler: "vide",
		SampleDur: uint32(frameTicks), Count: frames, SyncEvery: syncEvery,
		Color: videoColor,
		Payload: func(i int) []byte {
			return VideoAUSeeded(i, opts.Seed, opts.Faults[FaultColorConflict], false)
		},
	}
	audio := TrackSpec{
		TrackID: model.TrackAudioMain, Timescale: 48000, Handler: "soun",
		SampleDur: 1024, Count: frames * 3, SyncEvery: 0,
		Audio: &model.AudioDescriptor{Source: "mp4-stsd", ChannelCount: 2, SampleRate: 48000},
		Payload: func(i int) []byte {
			return AudioFrame(i, false)
		},
	}
	if opts.Faults[FaultAudioShort] {
		audio.Count = frames*3 - 1
	}

	mp4, err := BuildMP4([]TrackSpec{video, audio})
	if err != nil {
		return Scenario{}, fmt.Errorf("build mp4: %w", err)
	}
	s.MP4 = mp4

	if err := buildStreams(&s, opts, video, audio, frames, frameTicks, syncEvery); err != nil {
		return Scenario{}, err
	}

	// Subtitle cues, chapters, ID3, emsg, SEI are spread over the program and
	// placed on their own tracks so fault isolation can be observed.
	durationMS := int64(frames) * 1000 * opts.FPSDen / opts.FPSNum
	cues := []Cue{
		{StartMS: 250, EndMS: 1750, Text: "Opening slate"},
		{StartMS: 2100, EndMS: 3300, Text: "Wide shot of the harbor"},
		{StartMS: 3700, EndMS: 4600, Text: "Interview, take 2"},
	}
	if durationMS > 5000 {
		cues = append(cues, Cue{StartMS: durationMS - 1200, EndMS: durationMS - 200, Text: "Lower third"})
	}
	s.WebVTT = BuildWebVTT(cues)
	s.Chapters = BuildChapters([]Chapter{
		{StartMS: 0, Title: "Cold open"},
		{StartMS: 2000, Title: "Scene one"},
		{StartMS: 4000, Title: "Scene two"},
	})
	for i := 0; i < 4; i++ {
		s.ID3 = append(s.ID3, BuildID3(fmt.Sprintf("segment-%d", i), "PRIV", fmt.Sprintf("origin-%d", i)))
		s.ID3Ticks = append(s.ID3Ticks, int64(i)*90000)
	}
	for i := 0; i < 3; i++ {
		s.Emsg = append(s.Emsg, BuildEmsg(float64(i)+0.5, "urn:mediatimeline:cue", fmt.Sprintf("emsg-%d", i), 1000, uint32(i), []byte("payload")))
	}
	au := make([]byte, 0, 64)
	au = append(au, ingest.BuildSEINAL(500000, "midpoint")...)
	au = append(au, ingest.BuildSEINAL(2500000, "scene-marker")...)
	s.SEI = au

	s.Reference = Reference{
		VideoSamples: frames,
		AudioSamples: audio.Count,
		Events:       len(cues) + 3 + len(s.ID3) + len(s.Emsg) + 2,
		Description:  fmt.Sprintf("frames=%d fps=%d/%d ladder=%d segment=%dms", frames, opts.FPSNum, opts.FPSDen, opts.Ladder, opts.SegmentMS),
	}
	for i := 0; i < frames; i++ {
		s.Reference.VideoTimes = append(s.Reference.VideoTimes, int64(i)*frameTicks)
	}
	_ = rng
	s.Reference.KeyframeTicks = nil
	for i := 0; i < frames; i += syncEvery {
		s.Reference.KeyframeTicks = append(s.Reference.KeyframeTicks, int64(i)*frameTicks)
	}

	for i := 0; i < opts.Ladder; i++ {
		ov := LayerOverride{Index: i}
		if opts.Faults[FaultCorruptLayer] && i == opts.Ladder-1 {
			ov.Corrupt = true
		}
		s.LayerOverrides = append(s.LayerOverrides, ov)
	}
	return s, nil
}

// PacketSpec is a helper for building RTP packets.
type PacketSpec struct {
	Seq     uint16
	TS      uint32
	Marker  bool
	Payload []byte
}

func marshalRTP(p PacketSpec) ([]byte, error) {
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: p.Seq,
			Timestamp:      p.TS,
			SSRC:           0x0badcafe,
			Marker:         p.Marker,
		},
		Payload: p.Payload,
	}
	return pkt.Marshal()
}

// PipelineInput converts a built scenario into pipeline input. The fault set
// decides which competing declarations and damage flags are visible to the
// pipeline.
func PipelineInput(s Scenario, faults []Fault) pipeline.Input {
	set := map[Fault]bool{}
	for _, f := range faults {
		set[f] = true
	}
	in := pipeline.Input{
		MP4:       s.MP4,
		VideoRTP:  s.VideoRTP,
		AudioRTP:  s.AudioRTP,
		WebVTT:    s.WebVTT,
		Chapters:  s.Chapters,
		ID3:       s.ID3,
		ID3Ticks:  s.ID3Ticks,
		Emsg:      s.Emsg,
		SEI:       s.SEI,
		Ladder:    s.Options.Ladder,
		SegmentMS: s.Options.SegmentMS,
	}
	// The container declares BT.709 limited range for every fixture.
	in.ColorDecls = []model.ColorDeclaration{
		{Source: model.ColorFromContainer, Primaries: 1, Transfer: 1, Matrix: 1},
	}
	in.AudioLayouts = []model.AudioDescriptor{
		{Source: "mp4-stsd", ChannelCount: 2, SampleRate: 48000, Layout: "stereo"},
	}
	in.AudioRates = []uint32{48000}
	if set[FaultAudioOverlap] {
		in.AudioRates = append(in.AudioRates, 44100)
	}
	if set[FaultCorruptLayer] {
		in.CorruptLayer = true
	}
	return in
}
