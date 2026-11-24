package media

import (
	"bytes"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func TestBuildMP4HasTwoParsableTracksAndStableBytes(t *testing.T) {
	tracks := []Track{
		{ID: 1, Kind: "video", Timescale: 90000, Width: 16, Height: 9, Samples: []Sample{{Data: []byte("v0"), Delta: 3600, Sync: true}, {Data: []byte("v1"), Delta: 3600}}},
		{ID: 2, Kind: "audio", Timescale: 48000, Channels: 2, SampleRate: 48000, Samples: []Sample{{Data: make([]byte, 8), Delta: 480}}},
	}
	a, err := BuildMP4(tracks)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildMP4(tracks)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("MP4 output is not deterministic")
	}
	info, err := mp4.Probe(bytes.NewReader(a))
	if err != nil {
		t.Fatalf("go-mp4 probe: %v", err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2", len(info.Tracks))
	}
	if info.Tracks[0].Timescale != 90000 || info.Tracks[1].Timescale != 48000 {
		t.Fatalf("timescales=%d,%d", info.Tracks[0].Timescale, info.Tracks[1].Timescale)
	}
	if len(info.Tracks[0].Samples) != 2 || len(info.Tracks[1].Samples) != 1 {
		t.Fatalf("samples video=%d audio=%d", len(info.Tracks[0].Samples), len(info.Tracks[1].Samples))
	}
}
