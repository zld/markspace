package fixture

import mathrand "math/rand/v2"

// DemoSpec builds the self-contained acceptance sample from a seed. It always
// contains rotation, a legal crop, conflicting color descriptions (left
// unresolved), multichannel audio and mixed sample rates. Geometry and frame
// counts are seed-independent so output structure is stable; only a small
// numeric descriptor (the color primaries of the second description) varies.
func DemoSpec(seed int64) Spec {
	rng := mathrand.New(mathrand.NewPCG(uint64(seed), uint64(seed)^0xABCDEF))
	conflictPrimary := uint16(9 + rng.UintN(4)) // 9..12, deterministic per seed
	return Spec{
		Name: "demo",
		Video: VideoSpec{
			TrackID:   1,
			Timescale: 24000,
			Width:     64,
			Height:    48,
			Rotation:  90,
			Crop: &CropSpec{
				CleanWidthN: 60, CleanWidthD: 1,
				CleanHeightN: 44, CleanHeightD: 1,
			},
			Colors: []ColorSpec{
				{Type: "nclx", Primaries: 1, Transfer: 1, Matrix: 1},
				{Type: "nclx", Primaries: conflictPrimary,
					Transfer: 16, Matrix: 9, FullRange: true},
			},
			Delta:  1001,
			Frames: 12,
		},
		Audio: []AudioSpec{
			// 5.1 multichannel at 48 kHz.
			{TrackID: 2, Timescale: 48000, Channels: 6, SampleRate: 48000,
				SampleSize: 16, Delta: 1024, Samples: 12},
			// Stereo at a different sample rate to exercise normalization.
			{TrackID: 3, Timescale: 44100, Channels: 2, SampleRate: 44100,
				SampleSize: 16, Delta: 735, Samples: 12},
		},
	}
}

// DemoLayers returns the two bitrate representations for the acceptance run.
func DemoLayers() []LayerSpecShape {
	return []LayerSpecShape{
		{Name: "low", BitrateKbps: 400, Width: 32, Height: 24, SSRC: 1000, FramesPerSeg: 4},
		{Name: "high", BitrateKbps: 1200, Width: 64, Height: 48, SSRC: 2000, FramesPerSeg: 4},
	}
}

// LayerSpecShape mirrors fanout.LayerSpec without importing it (avoids a
// dependency cycle: fanout already imports render, which stays leaf-level).
type LayerSpecShape struct {
	Name         string
	BitrateKbps  int
	Width        int
	Height       int
	SSRC         uint32
	FramesPerSeg int
}
