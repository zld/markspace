package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func decodePNG(b []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(b))
}

func TestRenderDeterministicAndHashed(t *testing.T) {
	points := []SamplePoint{
		{TrackNumber: 1, StartNs: 0, EndNs: 120_000_000, Keyframe: true},
		{TrackNumber: 2, StartNs: 40_000_000, EndNs: 200_000_000},
	}
	opts := DefaultFrameOptions()
	run := func() ([]Frame, []FrameHash) {
		frames, err := RenderFrames(points, 0, 200_000_000, opts)
		if err != nil {
			t.Fatal(err)
		}
		return frames, HashFrames(frames)
	}
	frames1, hash1 := run()
	frames2, hash2 := run()
	if len(frames1) == 0 {
		t.Fatal("no frames rendered")
	}
	if len(frames1) != len(frames2) || len(hash1) != len(hash2) {
		t.Fatal("frame counts differ across runs")
	}
	for i := range hash1 {
		if hash1[i].SHA256 != hash2[i].SHA256 {
			t.Fatalf("frame %d hash differs across runs", i)
		}
		if hash1[i].TimeNs != frames1[i].TimeNs {
			t.Fatal("hash timestamp does not match frame")
		}
		if hash1[i].Bytes != len(frames1[i].PNG) {
			t.Fatal("hash byte count mismatch")
		}
	}
	if ManifestDigest(hash1) != ManifestDigest(hash2) {
		t.Fatal("manifest digest not deterministic")
	}
	// Distinct timestamps should normally produce distinct pixels/hashes.
	uniq := map[string]bool{}
	for _, h := range hash1 {
		uniq[h.SHA256] = true
	}
	if len(uniq) < 2 {
		t.Fatal("expected multiple distinct frames, got repeated identical hashes")
	}
}

func TestRenderPNGIsDecodable(t *testing.T) {
	frames, err := RenderFrames(nil, 0, 80_000_000, DefaultFrameOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 3 {
		t.Fatalf("want 3 frames at 0/40/80ms, got %d", len(frames))
	}
	for _, f := range frames {
		if _, err := decodePNG(f.PNG); err != nil {
			t.Fatalf("frame not a valid PNG: %v", err)
		}
		if f.Width != 64 || f.Height != 36 {
			t.Fatalf("unexpected geometry %dx%d", f.Width, f.Height)
		}
	}
}

func TestSummarizeAudioSilenceAndRange(t *testing.T) {
	points := []AudioPoint{
		{TrackNumber: 2, StartNs: 0, EndNs: 10, Payload: []byte{1, 2}},
		{TrackNumber: 2, StartNs: 10, EndNs: 20, Payload: []byte{0, 0, 0}}, // silent run 1
		{TrackNumber: 2, StartNs: 20, EndNs: 30, Payload: []byte{0, 0}},     // contiguous silence
		{TrackNumber: 2, StartNs: 30, EndNs: 40, Payload: []byte{9}},        // break
		{TrackNumber: 2, StartNs: 40, EndNs: 50, Payload: []byte{0}},        // silent run 2
		{TrackNumber: 3, StartNs: 5, EndNs: 15, Payload: []byte{0}},         // other track run 1
	}
	stats := SummarizeAudio(points)
	if len(stats) != 2 {
		t.Fatalf("want 2 track stats, got %d", len(stats))
	}
	var s2, s3 *AudioStats
	for i := range stats {
		switch stats[i].TrackNumber {
		case 2:
			s2 = &stats[i]
		case 3:
			s3 = &stats[i]
		}
	}
	if s2.SampleCount != 9 {
		t.Fatalf("want 9 audio bytes on track 2, got %d", s2.SampleCount)
	}
	if s2.SilentRuns != 2 {
		t.Fatalf("want 2 silent runs, got %d", s2.SilentRuns)
	}
	if s2.StartNs != 0 || s2.EndNs != 50 {
		t.Fatalf("range wrong: %d..%d", s2.StartNs, s2.EndNs)
	}
	if s3.SilentRuns != 1 {
		t.Fatalf("track 3 want 1 silent run, got %d", s3.SilentRuns)
	}
}

func TestSummarizeAudioEmpty(t *testing.T) {
	if got := SummarizeAudio(nil); len(got) != 0 {
		t.Fatalf("empty input must yield no stats, got %d", len(got))
	}
}
