package verify_test

import (
	"testing"

	"mediacore/internal/timeline"
	"mediacore/internal/transform"
	"mediacore/internal/verify"
)

func sample(track uint32, t int64, off int64) timeline.Sample {
	return timeline.Sample{TrackID: track, TimelineTime: t, TimelineDuration: 10, File: "a.fmp4", FileOffset: off, ByteLength: 8, SourceTime: t}
}

func frame(n int, t int64, off int64) transform.Frame {
	return transform.Frame{Number: n, TrackID: 1, TimelineTime: t, PixelSHA: "aa", SourceOffset: off}
}

func TestRunReportsCleanTimeline(t *testing.T) {
	samples := []timeline.Sample{sample(1, 0, 8), sample(2, 0, 24), sample(1, 10, 16)}
	m := verify.NewManifest(1, "cfg", "in", nil)
	frames := []transform.Frame{frame(0, 0, 8), frame(1, 0, 24), frame(2, 10, 16)}
	set := &transform.FrameSet{Frames: frames, Digest: "d"}
	m.AddFrames(set)
	in := verify.Input{
		FileSizes:    map[string]int64{"a.fmp4": 100},
		TrackSamples: map[uint32][]timeline.Sample{1: {samples[0], samples[2]}, 2: {samples[1]}},
		Merged:       samples,
		Frames:       frames,
		FrameDigest:  "d",
		Manifest:     m,
	}
	if failures := verify.Run(in); len(failures) != 0 {
		t.Fatalf("unexpected failures: %v", failures)
	}
}

func TestRunDetectsBackwardsTimeline(t *testing.T) {
	samples := []timeline.Sample{sample(1, 20, 8), sample(1, 10, 16)}
	in := verify.Input{
		FileSizes: map[string]int64{"a.fmp4": 100},
		Merged:    samples,
		Frames:    []transform.Frame{frame(0, 20, 8), frame(1, 10, 16)},
		Manifest:  verify.NewManifest(1, "", "", nil),
	}
	failures := verify.Run(in)
	if len(failures) == 0 {
		t.Fatal("backwards timeline not detected")
	}
}

func TestRunDetectsOutOfRangeSource(t *testing.T) {
	s := sample(1, 0, 96)
	s.ByteLength = 16 // 96+16 > 100
	in := verify.Input{
		FileSizes: map[string]int64{"a.fmp4": 100},
		Merged:    []timeline.Sample{s},
		Frames:    []transform.Frame{frame(0, 0, 96)},
		Manifest:  verify.NewManifest(1, "", "", nil),
	}
	if failures := verify.Run(in); len(failures) == 0 {
		t.Fatal("out of range source not detected")
	}
}

func TestRunDetectsMissingManifest(t *testing.T) {
	in := verify.Input{Merged: nil, Frames: nil}
	if failures := verify.Run(in); len(failures) != 1 {
		t.Fatalf("expected manifest failure, got %v", failures)
	}
}

func TestManifestSelfDigestDetectsTampering(t *testing.T) {
	m := verify.NewManifest(41, "cfg", "input", []verify.SourceEntry{{Name: "a.fmp4", Size: 10, SHA256: "abc"}})
	m.AddFrames(&transform.FrameSet{Frames: []transform.Frame{frame(0, 0, 8)}, Digest: "d"})
	path := t.TempDir() + "/manifest.json"
	if err := m.Write(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := verify.ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SHA256 == "" {
		t.Fatal("manifest self digest not written")
	}
	loaded.Seed = 99
	if loaded.SHA256 == loaded.Digest() {
		t.Fatal("tampering not detected")
	}
}
