package evidence

import (
	"bytes"
	"math"
	"testing"

	"mediacheck/internal/demux"
	"mediacheck/internal/testmedia"
)

func TestPixelStatsKnownValues(t *testing.T) {
	mean, std := PixelStats([]byte{0, 10, 20})
	if mean != 10 {
		t.Fatalf("mean = %v; want 10", mean)
	}
	// Population stddev of {0,10,20}: sqrt((100+0+100)/3)
	want := math.Sqrt(200.0 / 3.0)
	if math.Abs(std-want) > 1e-9 {
		t.Fatalf("stddev = %v; want %v", std, want)
	}
	if m, s := PixelStats(nil); m != 0 || s != 0 {
		t.Fatal("empty input must give zero stats")
	}
}

func TestAudioPeakDBFS(t *testing.T) {
	// Full-scale unsigned byte -> 0 dBFS.
	if got := AudioPeakDBFS([]byte{255}); math.Abs(got-0) > 1e-9 {
		t.Fatalf("full scale peak = %v; want 0 dBFS", got)
	}
	// Half scale (~128/255) must be negative and close to -6 dBFS.
	got := AudioPeakDBFS([]byte{128})
	if got >= 0 || got < -7 || got > -5 {
		t.Fatalf("half-scale peak = %v; want about -6 dBFS", got)
	}
	// Silence is negative infinity and never NaN.
	if got := AudioPeakDBFS([]byte{0, 0}); !math.IsInf(got, -1) {
		t.Fatalf("silence peak = %v; want -inf", got)
	}
}

func TestFrameHashChangesWithOrderAndContent(t *testing.T) {
	a := []string{HashBytes([]byte("a")), HashBytes([]byte("b"))}
	b := []string{HashBytes([]byte("b")), HashBytes([]byte("a"))}
	if ChainFrameHashes(a) == ChainFrameHashes(b) {
		t.Fatal("reordered frames must change the chained hash")
	}
}

func TestCollectStableAndMatchingAcrossRemux(t *testing.T) {
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	if err != nil {
		t.Fatal(err)
	}
	c, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(media.Container)
	read := func(tr *demux.Track, i int) ([]byte, error) {
		return c.ReadSample(reader, tr, i)
	}
	m1, err := Collect(c, media.Container, media.Container, read, read)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Collect(c, media.Container, media.Container, read, read)
	if err != nil {
		t.Fatal(err)
	}
	if m1.InputSHA256 != m2.InputSHA256 || m1.FrameHash != m2.FrameHash ||
		m1.PixelMean != m2.PixelMean || m1.AudioPeakDBFS != m2.AudioPeakDBFS {
		t.Fatal("metrics must be deterministic across collections")
	}
	if m1.InputSHA256 != m1.OutputSHA256 {
		t.Fatal("identical source/output containers should hash equal")
	}
	if len(m1.PerFrameHash) != 48 {
		t.Fatalf("frame hashes = %d; want 48", len(m1.PerFrameHash))
	}
	if math.IsNaN(m1.PixelMean) || math.IsNaN(m1.PixelStddev) {
		t.Fatal("pixel stats must not be NaN")
	}
	// A valid finite dBFS value must always be <= 0 dBFS (full-scale ceiling).
	if math.IsNaN(m1.AudioPeakDBFS) || m1.AudioPeakDBFS > 0 {
		t.Fatalf("audio peak %f is not a valid dBFS value", m1.AudioPeakDBFS)
	}
}
