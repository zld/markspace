package container

import (
	"os"
	"path/filepath"
	"testing"

	"mediarc/timebase"
)

func TestScanTruncatedKeepsEarlierChunks(t *testing.T) {
	dir := t.TempDir()
	fx := writeFixture(t, dir, 0)
	full, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	// Cut at the start of the final video chunk: moov is intact at the front,
	// so the index is readable but its last chunk offsets point past the new
	// EOF while the earlier chunks are still fully present.
	cut := len(full) - 12800
	path := filepath.Join(dir, "cut-tail.mp4")
	if err := os.WriteFile(path, full[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ScanFile(path, ScanOptions{})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if result.IndexComplete {
		t.Fatal("index should be incomplete after truncation")
	}
	if len(result.Divergences) == 0 {
		t.Fatal("expected a divergence for the truncated chunk")
	}
	var videoSamples int
	for _, track := range result.Tracks {
		if track.Kind != TrackVideo {
			continue
		}
		videoSamples = len(track.Samples)
		for _, sample := range track.Samples {
			if sample.Offset+int64(sample.Size) > result.Size {
				t.Fatalf("sample past EOF: %d+%d > %d", sample.Offset, sample.Size, result.Size)
			}
		}
	}
	// 20 video samples in chunks of 5: only the last chunk (offsets 15..19)
	// leaves the file, so the first three chunks must survive.
	if videoSamples != 15 {
		t.Fatalf("kept %d video samples, want 15", videoSamples)
	}
	// Retained samples keep their original timeline positions, computed the
	// same exact way the scanner does.
	for _, track := range result.Tracks {
		if track.Timescale == 0 {
			continue // boundary-recovered synthetic track
		}
		for _, sample := range track.Samples {
			delta := int64(3000)
			if track.Kind == TrackAudio {
				delta = 1024
			}
			want, err := timebase.NanosFromScale(int64(sample.Number-1)*delta, track.Timescale)
			if err != nil {
				t.Fatal(err)
			}
			if sample.StartNS != want {
				t.Fatalf("sample %d/%d starts at %d, want %d", track.ID, sample.Number, sample.StartNS, want)
			}
		}
	}
}

func TestScanSampleCap(t *testing.T) {
	dir := t.TempDir()
	fx := writeFixture(t, dir, 0)
	result, err := ScanFile(fx.path, ScanOptions{MaxSamplesPerTrack: 10})
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if result.IndexComplete {
		t.Fatal("capped scan should report an incomplete index")
	}
	for _, track := range result.Tracks {
		if len(track.Samples) > 10 {
			t.Fatalf("track %d exceeds cap: %d", track.ID, len(track.Samples))
		}
	}
}
