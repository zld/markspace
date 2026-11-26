package probe_test

import (
	"bytes"
	"image/png"
	"math"
	"path/filepath"
	"testing"

	"mediarepair/internal/probe"
	"mediarepair/internal/testfixture"
)

func TestPixelMeanMatchesAcceptanceValue(t *testing.T) {
	var total probe.PixelStats
	for f := 0; f < testfixture.FrameCount; f++ {
		b, err := testfixture.BuildFramePNG(f, testfixture.FrameSize, testfixture.FrameSize)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		total.Add(probe.ImagePixelStats(img))
	}
	r, g, b := total.Means()
	if r != 93 || g != 95.5 || b != 108.5 {
		t.Fatalf("means=%v,%v,%v", r, g, b)
	}
	if got := probe.FormatMeanRGB(r, g, b); got != "93,95.5,108.5" {
		t.Fatalf("format=%q", got)
	}
}

func TestWAVPeakAndOnsets(t *testing.T) {
	wav := testfixture.BuildBeatWAV(48000, 24000)
	st, err := probe.ParseWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	if st.SampleRate != 48000 || st.Channels != 1 {
		t.Fatalf("wav params=%+v", st)
	}
	if math.Abs(st.Peak-0.5) > 1e-9 {
		t.Fatalf("peak=%v want .5", st.Peak)
	}
	if got := math.Round(st.PeakDBFS*100) / 100; got != -6.02 {
		t.Fatalf("dbfs=%.2f rounded=%v want -6.02", st.PeakDBFS, got)
	}
	onsets := probe.DetectOnsets(st.Samples, 0.49, 6000)
	if len(onsets) != 3 || onsets[1] != 8000 || onsets[2] != 16000 {
		t.Fatalf("onsets=%v want 0,8000,16000", onsets)
	}
}

func TestReadGeneratedWAVAndAlignmentBound(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := probe.ReadWAV(filepath.Join(root, fx.Manifest.Audio.Path))
	if err != nil {
		t.Fatal(err)
	}
	visual := []int64{0, 166667, 333333}
	onsets := probe.DetectOnsets(st.Samples, 0.49, 6000)
	maxUS, _ := probe.AlignmentErrorUS(visual, onsets, st.SampleRate)
	if maxUS > 11.12 {
		t.Fatalf("align max=%.3f us", maxUS)
	}
}
