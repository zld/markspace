package fixture

import "continuity/media"

// ValidSpec returns a spec with aligned keyframes every 60 video samples
// (2 s at 30 fps / 90 kHz) plus 48 kHz stereo audio.
func ValidSpec() Spec {
	return Spec{Tracks: []TrackSpec{
		{ID: 1, Video: true, Timescale: 90000, SampleDelta: 3000,
			SampleCount: 180, SampleSize: 256, KeyframeEvery: 60,
			Width: 1280, Height: 720, ZeroDeltaAt: -1,
			Color: media.ColorMeta{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}},
		{ID: 2, Video: false, Timescale: 48000, SampleDelta: 1024,
			SampleCount: 281, SampleSize: 64, Channels: 2, SampleRate: 48000,
			ZeroDeltaAt: -1},
	}}
}

// MissingKeyframeSpec returns a spec whose video keyframes fall every
// 3 s, so a 2 s cut grid misses them.
func MissingKeyframeSpec() Spec {
	s := ValidSpec()
	s.Tracks[0].KeyframeEvery = 90 // 3 s at 30 fps
	return s
}

// AudioConflictSpec returns a spec with 6-channel 44.1 kHz audio.
func AudioConflictSpec() Spec {
	s := ValidSpec()
	s.Tracks[1].Channels = 6
	s.Tracks[1].SampleRate = 44100
	return s
}

// ColorConflictSpec returns a spec with BT.709 color replaced by BT.2020.
func ColorConflictSpec() Spec {
	s := ValidSpec()
	s.Tracks[0].Color = media.ColorMeta{Present: true, Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}
	return s
}
