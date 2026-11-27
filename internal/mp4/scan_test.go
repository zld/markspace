package mp4scan

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/fixgen"
)

func writeFixture(t *testing.T, tracks []fixgen.TrackSpec) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := fixgen.WriteMP4(f, tracks); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanTracksAndSamples(t *testing.T) {
	path := writeFixture(t, []fixgen.TrackSpec{
		{ID: 1, Kind: "audio", Timescale: 48000,
			Deltas: []uint32{1024, 1024, 1024}, Sizes: []uint32{300, 0, 310}},
		{ID: 2, Kind: "video", Timescale: 90000,
			Deltas: []uint32{3000, 3000, 0, 3000}, Sizes: []uint32{5000, 200, 200, 200},
			Keyframes: []uint32{1, 4}},
	})
	f, err := ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tracks) != 2 {
		t.Fatalf("got %d tracks", len(f.Tracks))
	}
	a, v := f.Tracks[0], f.Tracks[1]
	if a.Kind != "audio" || a.Clock.Timescale != 48000 || len(a.Samples) != 3 {
		t.Fatalf("audio track: %+v", a)
	}
	if a.Duration() != 3072 || a.TotalBytes() != 610 {
		t.Fatalf("audio dur=%d bytes=%d", a.Duration(), a.TotalBytes())
	}
	if len(a.Faults) != 1 || a.Faults[0].Offset != 1 {
		t.Fatalf("audio faults=%+v", a.Faults)
	}
	if v.Kind != "video" || len(v.Samples) != 4 {
		t.Fatalf("video track: %+v", v)
	}
	if v.Samples[1].Start != 3000 {
		t.Fatalf("sample 1 start=%d", v.Samples[1].Start)
	}
	if !v.Samples[0].Key || !v.Samples[3].Key || v.Samples[1].Key {
		t.Fatalf("keyframe flags wrong: %+v", v.Samples)
	}
	// zero delta -> anomalous timestamp fault at sample 2
	found := false
	for _, fl := range v.Faults {
		if fl.Offset == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("video faults=%+v", v.Faults)
	}
}

func TestAllKeyWhenNoStss(t *testing.T) {
	path := writeFixture(t, []fixgen.TrackSpec{
		{ID: 1, Kind: "audio", Timescale: 48000,
			Deltas: []uint32{1024, 1024}, Sizes: []uint32{10, 10}},
	})
	f, err := ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range f.Tracks[0].Samples {
		if !s.Key {
			t.Fatal("audio samples should all be key")
		}
	}
}
