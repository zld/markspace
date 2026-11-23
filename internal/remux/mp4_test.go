package remux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

func sampleInput() Input {
	dur, _ := clock.FromMillis(2000)
	return Input{
		DurationTick: dur,
		Tracks: []Track{
			{
				ID: 1, Kind: "video", Codec: "avc1", Timescale: 90000,
				SampleCount: 50, SampleDelta: 3600, Width: 320, Height: 180,
				Payload: []byte("VIDEO-SAMPLE-PAYLOAD"),
			},
			{
				ID: 2, Kind: "audio", Codec: "mp4a", Timescale: 48000,
				SampleCount: 96, SampleDelta: 1024, ChannelCount: 2,
				SampleRate: 48000, Payload: []byte("AUDIO-SAMPLE-PAYLOAD"),
			},
		},
	}
}

func TestWriteAtomicallyVerifiesAndHashes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.mp4")
	sum, err := WriteAtomically(path, sampleInput())
	if err != nil {
		t.Fatalf("WriteAtomically: %v", err)
	}
	if !sum.Verified || sum.SHA256 == "" {
		t.Fatalf("summary not verified: %+v", sum)
	}
	if sum.Tracks != 2 || sum.VideoSamples != 50 || sum.AudioSamples != 96 {
		t.Fatalf("sample counts wrong: %+v", sum)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("output missing: %v", err)
	}
	// No temporary files may remain in the output directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || strings.HasPrefix(e.Name(), ".remux-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestDeterministicHashAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	s1, err := WriteAtomically(filepath.Join(dir, "a.mp4"), sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := WriteAtomically(filepath.Join(dir, "b.mp4"), sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if s1.SHA256 != s2.SHA256 {
		t.Fatalf("same input produced different hashes:\n%s\n%s", s1.SHA256, s2.SHA256)
	}
	if s1.Size != s2.Size {
		t.Fatalf("file size diverged: %d vs %d", s1.Size, s2.Size)
	}
}

func TestValidationFailures(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		in   Input
		code string
	}{
		{"no tracks", Input{DurationTick: 90000}, "no_track"},
		{"zero timescale", func() Input {
			in := sampleInput()
			in.Tracks[0].Timescale = 0
			return in
		}(), "zero_timescale"},
		{"unsupported video codec", func() Input {
			in := sampleInput()
			in.Tracks[0].Codec = "vp09"
			return in
		}(), "codec"},
		{"no video", func() Input {
			in := sampleInput()
			in.Tracks = in.Tracks[1:]
			return in
		}(), "no_video"},
		{"zero duration", func() Input {
			in := sampleInput()
			in.DurationTick = 0
			return in
		}(), "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WriteAtomically(filepath.Join(dir, tc.name+".mp4"), tc.in)
			me, ok := mediaerr.As(err)
			if !ok || me.Code != tc.code {
				t.Fatalf("got %v, want code %s", err, tc.code)
			}
			// A failed write must not publish a destination file.
			if _, err := os.Stat(filepath.Join(dir, tc.name+".mp4")); !os.IsNotExist(err) {
				t.Fatal("failed remux must not leave a published file")
			}
		})
	}
}

func TestTrackDurationHelper(t *testing.T) {
	tr := Track{SampleCount: 50, SampleDelta: 3600}
	if got := trackDuration(tr); got != 180000 {
		t.Fatalf("trackDuration = %d, want 180000", got)
	}
}
