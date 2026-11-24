package mp4info

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeMP4 asks ffmpeg to wrap a generated sine-wave WAV into an AAC MP4 so the
// test exercises go-mp4 against a real, standards-conformant box tree.
func makeMP4(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	wav := filepath.Join(dir, "tone.wav")
	mp4 := filepath.Join(dir, "tone.m4a")
	gen := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-ar", "48000", "-ac", "2", wav)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("wav generation failed: %v\n%s", err, out)
	}
	enc := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-y",
		"-i", wav, "-c:a", "aac", "-movflags", "+faststart", mp4)
	if out, err := enc.CombinedOutput(); err != nil {
		t.Fatalf("mp4 generation failed: %v\n%s", err, out)
	}
	return mp4
}

func TestRead_TimelineAndSamples(t *testing.T) {
	dir := t.TempDir()
	mp4 := makeMP4(t, dir)
	info, err := Read(mp4)
	if err != nil {
		t.Fatal(err)
	}
	if info.Timescale == 0 {
		t.Fatal("timescale must be present")
	}
	if info.SampleCount == 0 {
		t.Fatal("sample count must be present")
	}
	// A 3-second clip must report a duration close to 3000 ms.
	if dms := info.DurationMS(); dms < 2500 || dms > 3500 {
		t.Fatalf("duration ms = %d, want ~3000", dms)
	}
	// Flattened deltas must cover exactly the declared sample count.
	if len(info.Deltas) != int(info.SampleCount) {
		t.Fatalf("delta entries = %d, samples = %d", len(info.Deltas), info.SampleCount)
	}
	tl := info.SampleTimeline()
	if len(tl) != int(info.SampleCount) || tl[0] != 0 {
		t.Fatalf("sample timeline wrong: len=%d first=%d", len(tl), tl[0])
	}
	// Cumulative timestamps must be strictly non-decreasing.
	for i := 1; i < len(tl); i++ {
		if tl[i] < tl[i-1] {
			t.Fatalf("sample timeline regressed at %d: %d < %d", i, tl[i], tl[i-1])
		}
	}
}

func TestRead_MissingFile(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.m4a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file must surface os error, got %v", err)
	}
}

func TestRead_NotAnMP4(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake.m4a")
	if err := os.WriteFile(fake, []byte("this is definitely not an mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(fake); !errors.Is(err, ErrNoTrack) {
		t.Fatalf("non-mp4 must fail with ErrNoTrack, got %v", err)
	}
}

func TestTimeline_DurationMSZeroTimescale(t *testing.T) {
	zero := TimelineInfo{}
	if zero.DurationMS() != 0 {
		t.Fatal("zero timescale must not divide by zero")
	}
}
