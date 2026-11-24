package engine

import (
	"testing"

	"mediasync/internal/manifest"
	"mediasync/internal/model"
)

// testManifest builds a two-track manifest in memory for unit tests that do
// not need generated MP4 or capture data.
func testManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		Version: manifest.Version, BaseTimescale: 1_000_000_000,
		Tracks: []manifest.Track{
			{
				ID: 1, Kind: model.KindVideo, Timescale: 90_000,
				StartTicks: 0, DurationTicks: 900_000, MediaFile: "cam1.mp4",
				Edit:      []manifest.Edit{{StartTicks: 0, DurationTicks: 900_000}},
				Keyframes: []int64{0, 90_000},
			},
			{
				ID: 2, Kind: model.KindAudio, Timescale: 48_000,
				StartTicks: 0, DurationTicks: 480_000, MediaFile: "cam1.mp4",
				Edit:      []manifest.Edit{{StartTicks: 0, DurationTicks: 480_000}},
				Keyframes: []int64{0},
			},
		},
	}
}
