package evidence

import (
	"math"
	"testing"

	"mediatimeline/internal/synthetic"
)

func TestRenderFrameDeterministic(t *testing.T) {
	samples := []synthetic.VideoSample{
		{Duration: 3000, Width: 8, Height: 4, Seed: 9},
		{Duration: 3000, Width: 8, Height: 4, Seed: 10},
	}
	raw := synthetic.BuildVideoFragment(1, 0, 0, samples)
	// Extract payloads via the documented 8-byte frame header layout.
	payloads := extractFramePayloads(t, raw)
	if len(payloads) != len(samples) {
		t.Fatalf("payloads = %d", len(payloads))
	}
	e1, err := RenderFrame(0, payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	e1b, err := RenderFrame(0, payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	if e1.SHA256 != e1b.SHA256 {
		t.Fatal("frame hash not deterministic")
	}
	if len(e1.SHA256) != 64 {
		t.Fatalf("hash length = %d", len(e1.SHA256))
	}
	if e1.PixelMeanR < 0 || e1.PixelMeanR > 255 || e1.PPMBytes <= 0 {
		t.Fatalf("bad evidence: %+v", e1)
	}
	e2, _ := RenderFrame(1, payloads[1])
	if e2.SHA256 == e1.SHA256 {
		t.Fatal("different seeds produced identical frames")
	}
}

func TestDecodeFrameRejectsTruncation(t *testing.T) {
	raw := synthetic.BuildVideoFragment(1, 0, 0, []synthetic.VideoSample{
		{Duration: 3000, Width: 4, Height: 4, Seed: 1},
	})
	payloads := extractFramePayloads(t, raw)
	if _, err := DecodeFrame(payloads[0][:4]); err == nil {
		t.Fatal("short payload accepted")
	}
	bad := append([]byte(nil), payloads[0]...)
	bad[0] = 'X'
	if _, err := DecodeFrame(bad); err == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestAudioStats(t *testing.T) {
	raw := synthetic.AudioFragment(2, 0, 0, 48000, 480, 3)
	pcm := extractMDAT(t, raw)
	stats, err := AnalyzeAudio(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SampleCount != 480 {
		t.Fatalf("samples = %d", stats.SampleCount)
	}
	if stats.PeakDBFS >= 0 || stats.PeakDBFS <= -60 {
		t.Fatalf("peak out of plausible range: %f", stats.PeakDBFS)
	}
	if math.IsInf(stats.RMSDBFS, -1) {
		t.Fatal("non-silent waveform reported as silence")
	}
	if _, err := AnalyzeAudio([]byte{0, 0, 1}); err == nil {
		t.Fatal("odd-length PCM accepted")
	}
	silence, err := AnalyzeAudio(make([]byte, 16))
	if err != nil || !math.IsInf(silence.PeakDBFS, -1) || FormatDBFS(silence.PeakDBFS) != "-inf" {
		t.Fatalf("silence handling wrong: %+v err=%v", silence, err)
	}
}
