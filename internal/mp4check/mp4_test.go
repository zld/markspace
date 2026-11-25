package mp4check_test

import (
	"testing"

	"assetcheck/internal/mp4check"
)

func TestAnalyzeMinimalTracks(t *testing.T) {
	data := mp4check.MinimalMP4(48000, 44100)
	info, err := mp4check.Analyze(data)
	if err != nil {
		t.Fatalf("analyze minimal mp4: %v", err)
	}
	if !info.HasFTYP || !info.HasMOOV {
		t.Fatalf("missing ftyp/moov: %+v", info)
	}
	if info.TrackCount != 2 {
		t.Fatalf("track count=%d, want 2 (%+v)", info.TrackCount, info.Tracks)
	}
	if info.Tracks[0].Timescale != 48000 || info.Tracks[1].Timescale != 44100 {
		t.Fatalf("timescales wrong: %+v", info.Tracks)
	}
	foundMDHD := false
	for _, p := range info.BoxTypes {
		if p == "moov/trak/mdia/mdhd" {
			foundMDHD = true
		}
	}
	if !foundMDHD {
		t.Fatalf("mdhd path not walked: %v", info.BoxTypes)
	}
}

func TestAnalyzeTruncatedMP4(t *testing.T) {
	data := mp4check.MinimalMP4(48000)
	if _, err := mp4check.Analyze(data[:len(data)-12]); err == nil {
		t.Fatal("expected structure error for truncated mp4")
	}
	if _, err := mp4check.Analyze([]byte("not an mp4 at all, just bytes")); err == nil {
		t.Fatal("expected error for garbage input")
	}
}
