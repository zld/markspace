package index

import (
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
)

func TestMarshalMP4SampleDescriptionAndSyncBox(t *testing.T) {
	f := &ingest.Frame{
		TrackID: 1, PTS: 0, Timebase: clock.RTP90K, Width: 16, Height: 9,
		PixelFormat: ingest.PixelRGB24, Pixels: make([]byte, 16*9*3),
	}
	desc, err := MarshalVisualSampleDescription(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(desc) < 8 || string(desc[4:8]) != "avc1" {
		t.Fatalf("sample description is not an avc1 box: % x", desc[:min(16, len(desc))])
	}

	stss, err := MarshalSyncSampleBox([]uint32{3, 1, 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(stss) < 8 || string(stss[4:8]) != "stss" {
		t.Fatalf("sync box is not stss: % x", stss[:min(16, len(stss))])
	}
}

func TestPackageMP4MetadataGroupsTracks(t *testing.T) {
	frames := []*ingest.Frame{
		{TrackID: 1, PTS: 0, Timebase: clock.RTP90K, Width: 2, Height: 2, KeyFrame: true, PixelFormat: ingest.PixelRGB24, Pixels: make([]byte, 12)},
		{TrackID: 2, PTS: 0, Timebase: clock.PTS48K, Width: 2, Height: 2, KeyFrame: true, PixelFormat: ingest.PixelRGB24, Pixels: make([]byte, 12)},
	}
	idx, err := Build(frames)
	if err != nil {
		t.Fatal(err)
	}
	mp4Meta, err := PackageMP4Metadata(frames, idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mp4Meta.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2", len(mp4Meta.Tracks))
	}
	for track, meta := range mp4Meta.Tracks {
		if len(meta.SampleDescription) == 0 || len(meta.SyncSampleBox) == 0 {
			t.Fatalf("track %d missing MP4 boxes: %+v", track, meta)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
