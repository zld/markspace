// Package testkit builds deterministic fixtures. This file defines the demo
// document shape used by the CLI.
package testkit

// DemoDocSpec returns the fixture the CLI's demo command uses: eight seconds
// of 1080p-style video and stereo audio with payloads large enough to fill a
// 4 MiB layer buffer several times, so backpressure is actually exercised.
func DemoDocSpec(seed int64) DocSpec {
	return DocSpec{
		DocType:          "matroska",
		ScaleNs:          1_000_000,
		SegmentUnknown:   true,
		ClusterUnknown:   true,
		ClipDurMs:        8_000,
		BlocksPerCluster: 24,
		Seed:             seed,
		Tracks: []TrackSpec{
			{Number: 1, Codec: "V_VP9", Name: "camera-a", Language: "und",
				FrameDurMs: 40, FrameBytes: 60_000, KeyEvery: 10},
			// The audio device clock runs slightly fast; repair must cancel
			// it without introducing a visible offset.
			{Number: 2, Codec: "A_OPUS", Name: "boom-1", Language: "und",
				FrameDurMs: 20, FrameBytes: 4_000, DriftPPM: 220},
		},
	}
}
