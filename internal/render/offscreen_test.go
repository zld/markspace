package render

import (
	"math"
	"testing"

	"mediasync/internal/sync"
)

func videoCue(i int) sync.AlignedCue {
	return sync.AlignedCue{Track: "video", Index: i, StartTick: int64(i) * 3000, EndTick: int64(i+1) * 3000}
}
func audioCue(i int) sync.AlignedCue {
	return sync.AlignedCue{Track: "audio", Index: i, StartTick: int64(i) * 90000, EndTick: int64(i+1) * 90000}
}

func TestRenderFrameDeterministicAndStats(t *testing.T) {
	r := NewRenderer(Config{Width: 8, Height: 8, AudioRate: 48000, MaxFrames: 100, MaxSamples: 100000}, 7)
	cue := videoCue(0)
	f1 := r.RenderFrame(cue)
	f2 := r.RenderFrame(cue)
	if f1.SHA256 != f2.SHA256 {
		t.Fatal("identical cue/seed must hash identically")
	}
	if len(f1.Pixels) != 64 || f1.Width != 8 || f1.Height != 8 {
		t.Fatalf("frame geometry wrong: %dx%d px=%d", f1.Width, f1.Height, len(f1.Pixels))
	}
	if f1.PixelMin > f1.PixelMax {
		t.Fatal("pixel min/max inverted")
	}
	if f1.PixelMean < 0 || f1.PixelMean > 255 || f1.PixelStd < 0 {
		t.Fatalf("pixel stats out of range: mean=%f std=%f", f1.PixelMean, f1.PixelStd)
	}
	// Verify min/max/mean independently against the pixels.
	var sum int
	minV, maxV := uint8(255), uint8(0)
	for _, p := range f1.Pixels {
		sum += int(p)
		if p < minV {
			minV = p
		}
		if p > maxV {
			maxV = p
		}
	}
	if minV != f1.PixelMin || maxV != f1.PixelMax {
		t.Fatalf("min/max mismatch: got %d/%d want %d/%d", f1.PixelMin, f1.PixelMax, minV, maxV)
	}
	if math.Abs(float64(sum)/64-f1.PixelMean) > 1e-9 {
		t.Fatalf("mean mismatch: %f vs %f", float64(sum)/64, f1.PixelMean)
	}
}

func TestRenderFrameVariesWithSeedAndIndex(t *testing.T) {
	cue := videoCue(1)
	a := NewRenderer(DefaultConfig(), 1).RenderFrame(cue)
	b := NewRenderer(DefaultConfig(), 2).RenderFrame(cue)
	if a.SHA256 == b.SHA256 {
		t.Fatal("different seeds must produce different frames")
	}
	r := NewRenderer(DefaultConfig(), 1)
	if r.RenderFrame(videoCue(0)).SHA256 == r.RenderFrame(videoCue(1)).SHA256 {
		t.Fatal("different frame indices must differ")
	}
}

func TestRenderAudioPeakAndLoudness(t *testing.T) {
	r := NewRenderer(Config{Width: 4, Height: 4, AudioRate: 48000, MaxFrames: 10, MaxSamples: 100000}, 7)
	a1 := r.RenderAudio(audioCue(0))
	a2 := r.RenderAudio(audioCue(0))
	if a1.SHA256 != a2.SHA256 {
		t.Fatal("audio rendering must be deterministic")
	}
	// One second at 48 kHz.
	if len(a1.Samples) != 48000 || len(a1.PCM) != 96000 {
		t.Fatalf("sample/pcm length wrong: %d %d", len(a1.Samples), len(a1.PCM))
	}
	if a1.PeakAmplitude <= 0 || a1.PeakAmplitude > 10000 {
		t.Fatalf("peak out of expected range: %d", a1.PeakAmplitude)
	}
	// Independently verify peak from samples and PCM encoding.
	var peak int16
	for _, s := range a1.Samples {
		av := s
		if av < 0 {
			av = -av
		}
		if av > peak {
			peak = av
		}
	}
	if peak != a1.PeakAmplitude {
		t.Fatalf("peak mismatch: %d vs %d", a1.PeakAmplitude, peak)
	}
	wantDB := 20 * math.Log10(float64(peak)/32768.0)
	if math.Abs(a1.PeakLoudness-wantDB) > 1e-9 {
		t.Fatalf("loudness = %f, want %f", a1.PeakLoudness, wantDB)
	}
	if a1.PeakLoudness >= 0 {
		t.Fatal("dBFS peak must be negative below full scale")
	}
}

func TestRenderAllCapsAndPartitions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxFrames = 3
	r := NewRenderer(cfg, 5)
	var cues []sync.AlignedCue
	for i := 0; i < 5; i++ {
		cues = append(cues, videoCue(i))
	}
	cues = append(cues, audioCue(0), audioCue(1))
	frames, audio := r.RenderAll(cues)
	if len(frames) != 3 {
		t.Fatalf("frame cap not honored: %d", len(frames))
	}
	if len(audio) != 2 {
		t.Fatalf("audio fragments = %d, want 2", len(audio))
	}
	// Subtitle cues produce neither frames nor audio.
	cues = append(cues, sync.AlignedCue{Track: "subtitle", Index: 0, StartTick: 0, EndTick: 100})
	f2, a2 := r.RenderAll(cues)
	if len(f2) != 3 || len(a2) != 2 {
		t.Fatalf("subtitle cue leaked into render output: %d %d", len(f2), len(a2))
	}
}

func TestRenderAllStableAcrossRuns(t *testing.T) {
	cues := []sync.AlignedCue{videoCue(0), videoCue(1), audioCue(0)}
	run := func() ([]string, []string) {
		r := NewRenderer(DefaultConfig(), 42)
		frames, audio := r.RenderAll(cues)
		fh := make([]string, len(frames))
		for i, f := range frames {
			fh[i] = f.SHA256
		}
		ah := make([]string, len(audio))
		for i, a := range audio {
			ah[i] = a.SHA256
		}
		return fh, ah
	}
	fh1, ah1 := run()
	fh2, ah2 := run()
	for i := range fh1 {
		if fh1[i] != fh2[i] {
			t.Fatal("frame hashes differ across runs")
		}
	}
	for i := range ah1 {
		if ah1[i] != ah2[i] {
			t.Fatal("audio hashes differ across runs")
		}
	}
}
