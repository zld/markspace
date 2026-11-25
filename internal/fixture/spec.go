package fixture

// Spec describes a deterministic generated MP4-like fixture.
type Spec struct {
	ID                string
	Width             uint32
	Height            uint32
	FPS               int
	VideoSamples      int
	VideoTimescale    uint32
	SampleDelta       uint32
	KeyframeEvery     int
	CompositionOffset int64
	IncludeAudio      bool
	AudioSamples      int
	OOBEdit           bool
}

// GoodSpec returns a small broadcast-like baseline. Tests can reduce VideoSamples.
func GoodSpec(id string) Spec {
	return Spec{
		ID: id, Width: 1920, Height: 1080, FPS: 30,
		VideoSamples: 240, VideoTimescale: 30000, SampleDelta: 1000,
		KeyframeEvery: 60, IncludeAudio: true,
	}
}

// DurationMS returns the integer millisecond duration represented by video samples.
func (s Spec) DurationMS() int {
	if s.FPS <= 0 {
		return 0
	}
	return s.VideoSamples * 1000 / s.FPS
}

func (s Spec) withDefaults() Spec {
	if s.Width == 0 {
		s.Width = 1920
	}
	if s.Height == 0 {
		s.Height = 1080
	}
	if s.FPS == 0 {
		s.FPS = 30
	}
	if s.VideoSamples == 0 {
		s.VideoSamples = 60
	}
	if s.VideoTimescale == 0 {
		s.VideoTimescale = uint32(s.FPS * 1000)
	}
	if s.SampleDelta == 0 {
		s.SampleDelta = uint32(s.VideoTimescale / uint32(s.FPS))
	}
	if s.KeyframeEvery == 0 {
		s.KeyframeEvery = s.VideoSamples
	}
	if s.IncludeAudio && s.AudioSamples == 0 {
		// AAC's common 1024-tick frames at 48 kHz: durationMS * 48 / 1024.
		s.AudioSamples = s.DurationMS()*48/1024 + 1
		if s.AudioSamples <= 0 {
			s.AudioSamples = 1
		}
	}
	return s
}

func videoSampleSize(index int, keyframe bool) uint32 {
	size := uint32(32768 + (index%7)*512)
	if keyframe {
		size += 12288
	}
	return size
}

func isKeyframe(index, every int) bool {
	if every <= 0 {
		return index == 0
	}
	return index%every == 0
}
