package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/media-check/media"
)

// writeFixture writes the shared two-track MOV used by pipeline tests.
func writeFixture(t *testing.T, dir string) string {
	t.Helper()
	fx, err := media.BuildFixture(media.FixtureOptions{
		Seed: 41,
		Tracks: []media.FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 24, SyncEvery: 3, Width: 640, Height: 360},
			{ID: 2, Codec: "mp4a", Lang: "eng", Timescale: 48000, Samples: 24},
		},
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	path := filepath.Join(dir, "source.mov")
	if err := os.WriteFile(path, fx.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var testTargets = []media.TrackTarget{
	{Kind: media.KindVideo, Required: true},
	{Kind: media.KindAudio, Required: true},
}

// runOpts returns the standard test run configuration.
func runOpts(src, out string) RunOptions {
	return RunOptions{
		Source: src, OutDir: out, Workers: 4, Queue: 8,
		SamplesPer: 6, Targets: testTargets, Seed: 41, Resume: true,
	}
}
